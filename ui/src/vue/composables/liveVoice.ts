import { ref } from "vue";
import { liveVoiceApi, type LiveVoiceConfig } from "../../services/api";

export const DEFAULT_LIVE_VOICE = "marin";

const liveVoice = ref<string>(
  (typeof window !== "undefined" && window.__SHELLEY_INIT__?.live_voice) || DEFAULT_LIVE_VOICE,
);

export function useLiveVoice() {
  return { liveVoice, refreshLiveVoice, setLiveVoice };
}

export async function refreshLiveVoice(): Promise<LiveVoiceConfig> {
  const res = await liveVoiceApi.get();
  liveVoice.value = res.voice;
  return res;
}

export async function setLiveVoice(voice: string): Promise<LiveVoiceConfig> {
  const res = await liveVoiceApi.set(voice);
  liveVoice.value = res.voice;
  return res;
}
