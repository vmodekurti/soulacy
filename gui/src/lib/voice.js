// Realtime voice helpers (Story 11, docs/VOICE_SPIKE.md). Pure functions —
// unit-tested in voice.test.js. The WebRTC glue in Chat.svelte stays thin;
// everything decidable lives here.

/** Panel states. */
export const VOICE_STATES = ['unavailable', 'idle', 'connecting', 'live', 'error']

/**
 * Voice panel state machine. Events: status(available), start, connected,
 * stop, fail, retry.
 */
export function nextVoiceState(state, event) {
  switch (event.type) {
    case 'status':
      return event.available ? (state === 'unavailable' ? 'idle' : state) : 'unavailable'
    case 'start':
      return state === 'idle' ? 'connecting' : state
    case 'connected':
      return state === 'connecting' ? 'live' : state
    case 'stop':
      return state === 'live' || state === 'connecting' ? 'idle' : state
    case 'fail':
      return state === 'unavailable' ? state : 'error'
    case 'retry':
      return state === 'error' ? 'idle' : state
    default:
      return state
  }
}

/** SDP exchange endpoint for OpenAI Realtime WebRTC. */
export function realtimeCallURL(model, baseURL = 'https://api.openai.com') {
  return `${baseURL}/v1/realtime/calls?model=${encodeURIComponent(model || 'gpt-realtime-mini')}`
}

/**
 * Classify a Realtime data-channel event into what the panel renders.
 * Returns {kind, text?, usage?}:
 *   user_transcript      — completed transcription of the user's speech
 *   assistant_delta      — streaming assistant transcript fragment
 *   assistant_done       — assistant turn finished
 *   usage                — token usage for the finished response
 *   other                — ignored
 */
export function classifyRealtimeEvent(evt) {
  if (!evt || typeof evt.type !== 'string') return { kind: 'other' }
  switch (evt.type) {
    case 'conversation.item.input_audio_transcription.completed':
      return { kind: 'user_transcript', text: evt.transcript || '' }
    case 'response.output_audio_transcript.delta':
    case 'response.audio_transcript.delta': // pre-GA event name
      return { kind: 'assistant_delta', text: evt.delta || '' }
    case 'response.output_audio_transcript.done':
    case 'response.audio_transcript.done':
      return { kind: 'assistant_done', text: evt.transcript || '' }
    case 'response.done': {
      const u = evt.response?.usage
      return u ? { kind: 'usage', usage: u } : { kind: 'other' }
    }
    default:
      return { kind: 'other' }
  }
}

/** Accumulate usage events into a session total. */
export function addUsage(total, usage) {
  const t = { input: total?.input || 0, output: total?.output || 0 }
  t.input += usage?.input_tokens || 0
  t.output += usage?.output_tokens || 0
  return t
}

/** Compact cost/usage label for the panel, '' when nothing used yet. */
export function voiceUsageLabel(total) {
  if (!total || (!total.input && !total.output)) return ''
  return `↑${total.input} ↓${total.output} tok`
}

/** Human hint for each panel state. */
export function voiceHint(state, detail = '') {
  switch (state) {
    case 'unavailable':
      return detail || 'Voice is not configured. Set voice.provider in config.yaml.'
    case 'idle':
      return 'Start a voice conversation'
    case 'connecting':
      return 'Connecting…'
    case 'live':
      return 'Live — click to stop'
    case 'error':
      return detail || 'Voice session failed — click to retry'
    default:
      return ''
  }
}
