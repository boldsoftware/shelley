package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"
)

// Customization records server-observed verification and deployment, not
// claims made by an agent. Generation and ArtifactPath never come from clients.
type Customization struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Commit         string `json:"commit"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Error          string `json:"error"`
	// Output is the tail of the verification's output; the list of all
	// conversations' states leaves it out.
	Output              string     `json:"output,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
	VerifiedAt          *time.Time `json:"verified_at,omitempty"`
	InstalledAt         *time.Time `json:"installed_at,omitempty"`
	RunningUnderSystemd bool       `json:"running_under_systemd"`
	// Finished is the conversation's own claim, made with its latest verification
	// request, that its work is done: only then does running the build
	// archive it, and only if no user message came after the request
	// (FinishedSequenceID is the conversation's last message then). Adopting
	// another build keeps both.
	Finished           bool  `json:"finished"`
	FinishedSequenceID int64 `json:"finished_sequence_id,omitempty"`
	// Response-only: the automatic restart into this installed build,
	// "pending" (in AutoRestartInMS of user idleness) or "busy" (once work
	// finishes); empty when none is planned.
	AutoRestart     string `json:"auto_restart,omitempty"`
	AutoRestartInMS *int64 `json:"auto_restart_in_ms,omitempty"`
	Generation      string `json:"-"`
	ArtifactPath    string `json:"-"`
	Archived        bool   `json:"-"`
}

// Statuses that still need a verified build. The server re-verifies the
// checkout HEAD for them and shares the outcome with every such conversation whose
// commit HEAD contains.
func (state *Customization) Unsettled() bool {
	switch state.Status {
	case "waiting", "stale", "failed":
		return !state.Archived
	}
	return false
}

const customizationSelect = `SELECT t.generation,t.artifact_path,t.data,c.archived
	FROM shelley_customizations t JOIN conversations c ON c.conversation_id=t.conversation_id`

func scanCustomization(scanner rowScanner) (*Customization, error) {
	var state Customization
	var data string
	if err := scanner.Scan(&state.Generation, &state.ArtifactPath, &data, &state.Archived); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (db *DB) GetCustomization(ctx context.Context, id string) (*Customization, error) {
	var state *Customization
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		state, err = scanCustomization(rx.QueryRow(customizationSelect+" WHERE t.conversation_id=?", id))
		return err
	})
	return state, err
}

func (db *DB) ListCustomizations(ctx context.Context) ([]Customization, error) {
	states := []Customization{}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query(customizationSelect + " ORDER BY t.conversation_id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			state, err := scanCustomization(rows)
			if err != nil {
				return err
			}
			states = append(states, *state)
		}
		return rows.Err()
	})
	return states, err
}

func (db *DB) HasVerifyingCustomization(ctx context.Context) (bool, error) {
	var active bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT EXISTS(SELECT 1 FROM shelley_customizations WHERE status='verifying')").Scan(&active)
	})
	return active, err
}

func writeCustomization(tx *Tx, state *Customization) error {
	switch state.Status {
	case "waiting", "verifying", "ready", "stale", "failed", "installed", "restarting", "running":
	default:
		return fmt.Errorf("%w: unknown customization status %q", ErrInvalid, state.Status)
	}
	state.UpdatedAt = tx.Now.UTC()
	state.Output = BoundedOutput(state.Output)
	state.AutoRestart, state.AutoRestartInMS = "", nil
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO shelley_customizations(conversation_id,status,generation,artifact_path,data)
		VALUES(?,?,?,?,?) ON CONFLICT(conversation_id) DO UPDATE SET
		status=excluded.status,generation=excluded.generation,artifact_path=excluded.artifact_path,data=excluded.data`,
		state.ConversationID, state.Status, state.Generation, state.ArtifactPath, string(data))
	return err
}

// customizationRequested requires a project conversation (the server checks
// that the project is Shelley's own, see customizationDirectory) and records
// the conversation's last message for a finished claim.
func customizationRequested(tx *Tx, state *Customization) error {
	var eligible bool
	if err := tx.QueryRow(`SELECT EXISTS(`+workspaceConversationQuery+` AND conversation_id=? AND project!='')`, state.ConversationID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return fmt.Errorf("%w: only conversations in the Shelley project can verify customizations", ErrInvalid)
	}
	if !state.Finished {
		return nil
	}
	return tx.QueryRow("SELECT COALESCE(MAX(sequence_id),0) FROM messages WHERE conversation_id=?", state.ConversationID).Scan(&state.FinishedSequenceID)
}

func (db *DB) BeginCustomization(ctx context.Context, id, commit string, finished bool) (*Customization, error) {
	state := &Customization{ConversationID: id, Commit: commit, Status: "verifying", Finished: finished, Generation: uuid.NewString()}
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := customizationRequested(tx, state); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM shelley_customizations WHERE status IN ('verifying','restarting'))").Scan(&active); err != nil {
			return err
		}
		if active {
			return fmt.Errorf("%w: customization verification or restart is in progress", ErrConflict)
		}
		return writeCustomization(tx, state)
	})
	return state, err
}

// UpdateCustomization preserves concurrent output writes and rejects late
// workers from an earlier verification. The callback must not perform I/O.
func (db *DB) UpdateCustomization(ctx context.Context, id, generation string, update func(*Customization)) (*Customization, error) {
	var state *Customization
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) (err error) {
		state, err = updateCustomization(tx, id, generation, update)
		return err
	})
	return state, err
}

func updateCustomization(tx *Tx, id, generation string, update func(*Customization)) (*Customization, error) {
	state, err := scanCustomization(tx.QueryRow(customizationSelect+" WHERE t.conversation_id=?", id))
	if err != nil {
		return nil, err
	}
	if state.Generation != generation {
		return nil, fmt.Errorf("%w: customization verification was superseded", ErrConflict)
	}
	update(state)
	return state, writeCustomization(tx, state)
}

// AdoptCustomization gives a conversation the outcome of a build that contains its
// commit. A conversation still needing a build takes any outcome; a ready or installed
// conversation only upgrades to a build at least as far along (ready < installed <
// running). Adopting the running build applies the conversation's work, so a
// finished conversation is archived like after an approved restart.
func (db *DB) AdoptCustomization(ctx context.Context, id, generation string, source *Customization) (*Customization, error) {
	progress := map[string]int{"ready": 1, "installed": 2, "running": 3}
	var state *Customization
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) (err error) {
		adopted := false
		state, err = updateCustomization(tx, id, generation, func(current *Customization) {
			upgrade := progress[current.Status] != 0 && progress[source.Status] >= progress[current.Status]
			if !current.Unsettled() && !upgrade {
				return
			}
			adopted = true
			current.Status, current.Commit, current.Error, current.Output = source.Status, source.Commit, source.Error, source.Output
			current.ArtifactPath, current.ArtifactSHA256 = source.ArtifactPath, source.ArtifactSHA256
			current.VerifiedAt, current.InstalledAt = source.VerifiedAt, source.InstalledAt
		})
		if err != nil || !adopted || state.Status != "running" {
			return err
		}
		return archiveAppliedConversation(ctx, tx, state)
	})
	return state, err
}

// archiveAppliedConversation archives a conversation once the build carrying its work runs,
// but only if the conversation said its work was finished when it last asked for
// verification, the user has not written to it since, and it is idle: no turn,
// queued message, working subagent, or background job. Containment alone never archives a conversation, so a history
// rewrite or merge that makes a build contain every conversation's commits archives
// nothing by itself. The Assistant is never archived.
func archiveAppliedConversation(ctx context.Context, tx *Tx, state *Customization) error {
	if !state.Finished {
		return nil
	}
	// A later user message (not a tool result) reopens the conversation.
	var keep bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspace WHERE assistant_id=?) OR EXISTS(SELECT 1 FROM messages m
		WHERE conversation_id=? AND type='user' AND sequence_id>? AND `+notToolResult+`)`,
		state.ConversationID, state.ConversationID, state.FinishedSequenceID).Scan(&keep); err != nil || keep {
		return err
	}
	conversation, err := generated.New(tx.Conn()).GetConversation(ctx, state.ConversationID)
	if err != nil {
		return err
	}
	pending, err := pendingWork(tx, &conversation)
	if err != nil || pending {
		return err
	}
	_, err = generated.New(tx.Conn()).ArchiveConversation(ctx, state.ConversationID)
	return err
}

// RecoverCustomizations is called once per server process, before accepting
// verification. Only an executable digest match proves a build is running; any
// start (an approved restart, or an unplanned one picking up the installed
// binary) applies the work of every conversation whose verified build now runs; those
// whose work is finished are archived in the same transaction (see
// archiveAppliedConversation). installedSHA is the digest of the executable on disk.
func (db *DB) RecoverCustomizations(ctx context.Context, runningSHA, installedSHA string) error {
	states, err := db.ListCustomizations(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		running := runningSHA != "" && runningSHA == state.ArtifactSHA256
		switch {
		case state.Status == "verifying", state.Status == "restarting", state.Status == "installed", state.Status == "running",
			state.Status == "ready" && running:
		default:
			continue
		}
		err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			current, err := updateCustomization(tx, state.ConversationID, state.Generation, func(current *Customization) {
				switch {
				case current.Status == "verifying":
					// Interrupted, not failed: the server verifies it again.
					current.Status, current.Error = "waiting", ""
				case current.Status == "running":
					if !running {
						current.Status, current.Error = "stale", "The running executable has changed since installation."
					}
				case running:
					current.Status, current.Error = "running", ""
				case current.Status == "restarting":
					current.Status, current.Error = "failed", "Restart did not start the verified executable; its SHA-256 does not match."
				case current.Status == "installed" && current.ArtifactSHA256 != installedSHA:
					current.Status, current.Error = "stale", "The installed executable has changed since installation."
				}
			})
			if err != nil || state.Status == "running" || current.Status != "running" {
				return err
			}
			return archiveAppliedConversation(ctx, tx, current)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// WaitForCustomization queues a conversation behind the verification in progress.
// The server verifies the checkout HEAD next and shares the outcome with it.
func (db *DB) WaitForCustomization(ctx context.Context, id, commit string, finished bool) (*Customization, error) {
	state := &Customization{ConversationID: id, Commit: commit, Status: "waiting", Finished: finished, Generation: uuid.NewString()}
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := customizationRequested(tx, state); err != nil {
			return err
		}
		var status string
		err := tx.QueryRow("SELECT status FROM shelley_customizations WHERE conversation_id=?", id).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if status == "verifying" || status == "restarting" {
			return fmt.Errorf("%w: this conversation's customization is already %s", ErrConflict, status)
		}
		return writeCustomization(tx, state)
	})
	return state, err
}
