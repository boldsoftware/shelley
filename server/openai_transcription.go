package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
	"shelley.exe.dev/models"
)

const (
	maxTranscriptionUpload    = 25_000_000
	maxTranscriptionErrorBody = 64 << 10
	maxTranscriptionResponse  = 16 << 20
)

type transcriptionResult struct {
	Text            string
	Model           string
	TimestampsModel string
	TimestampsPath  string
}

type transcriptionAPIOptions struct {
	Model                  string
	ResponseFormat         string
	TimestampGranularities []string
}

var (
	textTranscription = transcriptionAPIOptions{
		Model:          models.TranscriptionTextModel,
		ResponseFormat: "json",
	}
	timestampTranscription = transcriptionAPIOptions{
		Model:                  models.TranscriptionTimestampsModel,
		ResponseFormat:         "verbose_json",
		TimestampGranularities: []string{"word", "segment"},
	}
)

type recordingTranscriber interface {
	// Available reports why recordings cannot be transcribed, or nil.
	Available() error
	Transcribe(context.Context, string, string, bool) (transcriptionResult, error)
}

type transcriptionModelProvider interface {
	GetTranscriptionModels(string) ([]models.TranscriptionModel, error)
}

type openAIRecordingTranscriber struct {
	client       *http.Client
	resolveModel func(string) (models.TranscriptionModel, error)
	mediaRun     mediaCommandRunner
}

type transcriptionAPIResponse struct {
	Text string
	Body []byte
}

type transcriptionHTTPError struct {
	StatusCode int
	Status     string
	Message    string
}

type transcriptionModelError struct {
	Model string
	Err   error
}

func (e *transcriptionModelError) Error() string { return e.Err.Error() }
func (e *transcriptionModelError) Unwrap() error { return e.Err }

func withTranscriptionModel(model string, err error) error {
	if err == nil {
		return nil
	}
	return &transcriptionModelError{Model: model, Err: err}
}

func (e *transcriptionHTTPError) Error() string {
	return fmt.Sprintf("transcription failed (%s): %s", e.Status, e.Message)
}

// With predictableOnly every route is ignored, as real LLM models are.
func newOpenAIRecordingTranscriber(provider LLMProvider, predictableOnly bool) recordingTranscriber {
	return &openAIRecordingTranscriber{
		client: http.DefaultClient,
		resolveModel: func(modelName string) (models.TranscriptionModel, error) {
			modelProvider, ok := provider.(transcriptionModelProvider)
			if !ok || predictableOnly {
				return models.TranscriptionModel{}, errTranscriptionModelUnavailable(modelName)
			}
			available, err := modelProvider.GetTranscriptionModels(modelName)
			if err != nil {
				return models.TranscriptionModel{}, fmt.Errorf("load transcription model %q: %w", modelName, err)
			}
			return selectTranscriptionModel(modelName, available)
		},
		mediaRun: runMediaCommand,
	}
}

// selectTranscriptionModel prefers an exe.dev LLM integration (the llm one
// first), then a custom model, then a route derived from OpenAI credentials.
func selectTranscriptionModel(modelName string, available []models.TranscriptionModel) (models.TranscriptionModel, error) {
	var selected *models.TranscriptionModel
	best := 0
	for i := range available {
		if available[i].Model != modelName || available[i].Endpoint == "" {
			continue
		}
		if rank := transcriptionRouteRank(available[i]); rank > best {
			selected, best = &available[i], rank
		}
	}
	if selected == nil {
		return models.TranscriptionModel{}, errTranscriptionModelUnavailable(modelName)
	}
	return *selected, nil
}

func transcriptionRouteRank(route models.TranscriptionModel) int {
	switch parsed, err := url.Parse(route.Endpoint); {
	case route.FromCredentials:
		return 1
	case route.Source == models.SourceCustomLabel:
		return 2
	case err == nil && strings.HasPrefix(parsed.Hostname(), "llm.int."):
		return 4
	default:
		return 3
	}
}

var errNoTranscriptionRoute = errors.New("set OPENAI_API_KEY or connect an exe.dev LLM integration")

func errTranscriptionModelUnavailable(modelName string) error {
	return fmt.Errorf("transcription model %q is not available; %w", modelName, errNoTranscriptionRoute)
}

// Available checks for a timing model too: screen and review recordings
// need word timings.
func (t *openAIRecordingTranscriber) Available() error {
	if _, err := t.resolveModel(textTranscription.Model); err != nil {
		return err
	}
	_, err := t.deepgramRoute()
	if err == nil {
		return nil
	}
	if !errors.Is(err, errNoTranscriptionRoute) {
		return err
	}
	_, err = t.resolveModel(timestampTranscription.Model)
	return err
}

// deepgramRoute returns the Deepgram route that times words, preferred over
// whisper-1 because its timings are tighter and arrive sooner. An error
// wrapping errNoTranscriptionRoute means there is none.
func (t *openAIRecordingTranscriber) deepgramRoute() (models.TranscriptionModel, error) {
	route, err := t.resolveModel(models.TranscriptionDeepgramModel)
	if err == nil && !route.Deepgram {
		// An OpenAI-compatible route that happens to share the name.
		err = errTranscriptionModelUnavailable(models.TranscriptionDeepgramModel)
	}
	return route, err
}

func (t *openAIRecordingTranscriber) Transcribe(ctx context.Context, mediaPath, prompt string, timestamps bool) (transcriptionResult, error) {
	uploadPath, cleanup, err := t.prepareUpload(ctx, mediaPath)
	if err != nil {
		return transcriptionResult{}, err
	}
	defer cleanup()

	if !timestamps {
		response, err := t.transcribe(ctx, uploadPath, prompt, textTranscription)
		if err != nil {
			return transcriptionResult{}, withTranscriptionModel(textTranscription.Model, err)
		}
		return transcriptionResult{Text: response.Text, Model: textTranscription.Model}, nil
	}

	deepgram, err := t.deepgramRoute()
	timingModel := models.TranscriptionDeepgramModel
	switch {
	case errors.Is(err, errNoTranscriptionRoute):
		timingModel = timestampTranscription.Model
	case err != nil:
		return transcriptionResult{}, err
	}
	var transcriptResponse transcriptionAPIResponse
	var timings []byte
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		transcriptResponse, err = t.transcribe(groupContext, uploadPath, prompt, textTranscription)
		return withTranscriptionModel(textTranscription.Model, err)
	})
	group.Go(func() error {
		var err error
		if deepgram.Deepgram {
			timings, err = t.deepgramTimings(groupContext, deepgram, uploadPath)
		} else {
			timings, err = t.whisperTimings(groupContext, uploadPath, prompt)
		}
		return withTranscriptionModel(timingModel, err)
	})
	if err := group.Wait(); err != nil {
		return transcriptionResult{}, err
	}

	timestampsPath := mediaPath + ".timestamps.json"
	if err := os.WriteFile(timestampsPath, timings, 0o600); err != nil {
		return transcriptionResult{}, fmt.Errorf("write transcription timestamps: %w", err)
	}
	return transcriptionResult{
		Text:            transcriptResponse.Text,
		Model:           textTranscription.Model,
		TimestampsModel: timingModel,
		TimestampsPath:  timestampsPath,
	}, nil
}

// whisperTimings returns whisper-1's verbose JSON, which has the shape of
// speechTimings.
func (t *openAIRecordingTranscriber) whisperTimings(ctx context.Context, mediaPath, prompt string) ([]byte, error) {
	response, err := t.transcribe(ctx, mediaPath, prompt, timestampTranscription)
	if err != nil {
		return nil, err
	}
	if err := validateTimestampResponse(response.Body); err != nil {
		return nil, err
	}
	return response.Body, nil
}

// deepgramTimings transcribes mediaPath at the Deepgram route and returns its
// word and sentence timings as speechTimings JSON.
func (t *openAIRecordingTranscriber) deepgramTimings(ctx context.Context, route models.TranscriptionModel, mediaPath string) ([]byte, error) {
	endpoint, err := url.Parse(route.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Deepgram endpoint: %w", err)
	}
	// smart_format adds punctuation and the paragraphs that hold sentence
	// timings. The speaker's language is unknown, and multi covers the most
	// common ones without automatic detection, which the gateway rejects.
	endpoint.RawQuery = url.Values{
		"model":        {models.TranscriptionDeepgramModel},
		"smart_format": {"true"},
		"language":     {"multi"},
	}.Encode()
	route.Endpoint = endpoint.String()
	media, err := os.ReadFile(mediaPath)
	if err != nil {
		return nil, fmt.Errorf("read recording: %w", err)
	}
	body, err := t.post(ctx, route, "application/octet-stream", media)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Results struct {
			Channels []struct {
				Alternatives []struct {
					// Words is nil, not empty, when the response is not
					// a transcription.
					Words []struct {
						PunctuatedWord string  `json:"punctuated_word"`
						Start          float64 `json:"start"`
						End            float64 `json:"end"`
					} `json:"words"`
					Paragraphs struct {
						Paragraphs []struct {
							Sentences []timedSegment `json:"sentences"`
						} `json:"paragraphs"`
					} `json:"paragraphs"`
				} `json:"alternatives"`
			} `json:"channels"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode Deepgram response: %w", err)
	}
	channels := decoded.Results.Channels
	if len(channels) == 0 || len(channels[0].Alternatives) == 0 || channels[0].Alternatives[0].Words == nil {
		return nil, errors.New("Deepgram response has no words")
	}
	alt := channels[0].Alternatives[0]
	timings := speechTimings{Words: []timedWord{}, Segments: []timedSegment{}}
	for _, word := range alt.Words {
		timings.Words = append(timings.Words, timedWord{Word: word.PunctuatedWord, Start: word.Start, End: word.End})
	}
	for _, paragraph := range alt.Paragraphs.Paragraphs {
		timings.Segments = append(timings.Segments, paragraph.Sentences...)
	}
	return json.Marshal(timings)
}

func (t *openAIRecordingTranscriber) prepareUpload(ctx context.Context, mediaPath string) (string, func(), error) {
	info, err := os.Stat(mediaPath)
	if err != nil {
		return "", func() {}, fmt.Errorf("inspect recording upload: %w", err)
	}
	if info.Size() <= maxTranscriptionUpload {
		return mediaPath, func() {}, nil
	}
	if t.mediaRun == nil {
		return "", func() {}, errors.New("oversized recording preparation requires a media command runner")
	}

	file, err := os.CreateTemp("", "shelley-transcription-*.m4a")
	if err != nil {
		return "", func() {}, fmt.Errorf("create prepared transcription audio: %w", err)
	}
	preparedPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(preparedPath)
		return "", func() {}, fmt.Errorf("close prepared transcription audio: %w", err)
	}
	cleanup := func() { _ = os.Remove(preparedPath) }
	if _, err := t.mediaRun(ctx, "ffmpeg", "-y", "-i", mediaPath, "-map", "0:a:0", "-vn", "-c:a", "aac", "-b:a", "64k", preparedPath); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("prepare oversized recording audio: %w", err)
	}
	preparedInfo, err := os.Stat(preparedPath)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("inspect prepared transcription audio: %w", err)
	}
	if preparedInfo.Size() > maxTranscriptionUpload {
		cleanup()
		return "", func() {}, errors.New("prepared recording is still larger than 25 MB; split it or use the transcribing-audio skill for chunked transcription")
	}
	return preparedPath, cleanup, nil
}

func validateTimestampResponse(body []byte) error {
	var decoded struct {
		Words    []json.RawMessage `json:"words"`
		Segments []json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode Whisper timestamp response: %w", err)
	}
	if decoded.Words == nil {
		return errors.New(`Whisper timestamp response field "words" must be an array`)
	}
	if decoded.Segments == nil {
		return errors.New(`Whisper timestamp response field "segments" must be an array`)
	}
	return nil
}

func (t *openAIRecordingTranscriber) transcribe(ctx context.Context, mediaPath, prompt string, options transcriptionAPIOptions) (transcriptionAPIResponse, error) {
	model, err := t.resolveModel(options.Model)
	if err != nil {
		return transcriptionAPIResponse{}, err
	}
	media, err := os.Open(mediaPath)
	if err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("open recording: %w", err)
	}
	defer media.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", options.Model); err != nil {
		return transcriptionAPIResponse{}, err
	}
	if err := writer.WriteField("response_format", options.ResponseFormat); err != nil {
		return transcriptionAPIResponse{}, err
	}
	for _, granularity := range options.TimestampGranularities {
		if err := writer.WriteField("timestamp_granularities[]", granularity); err != nil {
			return transcriptionAPIResponse{}, err
		}
	}
	if err := writer.WriteField("prompt", prompt); err != nil {
		return transcriptionAPIResponse{}, err
	}
	part, err := writer.CreateFormFile("file", filepath.Base(mediaPath))
	if err != nil {
		return transcriptionAPIResponse{}, err
	}
	if _, err := io.Copy(part, media); err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("read recording: %w", err)
	}
	if err := writer.Close(); err != nil {
		return transcriptionAPIResponse{}, err
	}

	responseBody, err := t.post(ctx, model, writer.FormDataContentType(), body.Bytes())
	if err != nil {
		return transcriptionAPIResponse{}, err
	}
	requireText := options.ResponseFormat != "verbose_json"
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("decode transcription response: %w", err)
	}
	decoded.Text = strings.TrimSpace(decoded.Text)
	if requireText && decoded.Text == "" {
		return transcriptionAPIResponse{}, errors.New("OpenAI transcription returned an empty transcript")
	}
	return transcriptionAPIResponse{Text: decoded.Text, Body: responseBody}, nil
}

// post sends body to model's endpoint and returns the successful response
// body.
func (t *openAIRecordingTranscriber) post(ctx context.Context, model models.TranscriptionModel, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, model.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if model.APIKey != "" && model.APIKey != "implicit" {
		req.Header.Set("Authorization", "Bearer "+model.APIKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTranscriptionResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read transcription response: %w", err)
	}
	if len(responseBody) > maxTranscriptionResponse {
		return nil, errors.New("transcription response exceeded 16 MiB")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return responseBody, nil
	}
	errorBody := responseBody
	if len(errorBody) > maxTranscriptionErrorBody {
		errorBody = errorBody[:maxTranscriptionErrorBody]
	}
	// OpenAI nests the message under "error"; Deepgram uses err_msg.
	var apiError struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		ErrMsg string `json:"err_msg"`
	}
	_ = json.Unmarshal(errorBody, &apiError)
	return nil, &transcriptionHTTPError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Message:    cmp.Or(apiError.Error.Message, apiError.ErrMsg, strings.TrimSpace(string(errorBody)), resp.Status),
	}
}
