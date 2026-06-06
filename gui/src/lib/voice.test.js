import { describe, it, expect } from 'vitest'
import {
  nextVoiceState, realtimeCallURL, classifyRealtimeEvent,
  addUsage, voiceUsageLabel, voiceHint,
} from './voice.js'

describe('nextVoiceState', () => {
  it('walks the happy path idle → connecting → live → idle', () => {
    let s = 'idle'
    s = nextVoiceState(s, { type: 'start' })
    expect(s).toBe('connecting')
    s = nextVoiceState(s, { type: 'connected' })
    expect(s).toBe('live')
    s = nextVoiceState(s, { type: 'stop' })
    expect(s).toBe('idle')
  })

  it('status toggles availability', () => {
    expect(nextVoiceState('unavailable', { type: 'status', available: true })).toBe('idle')
    expect(nextVoiceState('idle', { type: 'status', available: false })).toBe('unavailable')
    expect(nextVoiceState('live', { type: 'status', available: true })).toBe('live')
  })

  it('failures land in error and retry recovers', () => {
    expect(nextVoiceState('connecting', { type: 'fail' })).toBe('error')
    expect(nextVoiceState('error', { type: 'retry' })).toBe('idle')
    expect(nextVoiceState('unavailable', { type: 'fail' })).toBe('unavailable')
  })

  it('start only fires from idle', () => {
    expect(nextVoiceState('unavailable', { type: 'start' })).toBe('unavailable')
    expect(nextVoiceState('live', { type: 'start' })).toBe('live')
  })
})

describe('realtimeCallURL', () => {
  it('builds the SDP exchange URL with the model', () => {
    expect(realtimeCallURL('gpt-realtime-mini'))
      .toBe('https://api.openai.com/v1/realtime/calls?model=gpt-realtime-mini')
  })
  it('defaults the model and encodes it', () => {
    expect(realtimeCallURL('')).toContain('model=gpt-realtime-mini')
    expect(realtimeCallURL('a b')).toContain('model=a%20b')
  })
})

describe('classifyRealtimeEvent', () => {
  it('maps user transcription completion', () => {
    expect(classifyRealtimeEvent({
      type: 'conversation.item.input_audio_transcription.completed',
      transcript: 'hello there',
    })).toEqual({ kind: 'user_transcript', text: 'hello there' })
  })

  it('maps assistant transcript deltas (GA and pre-GA names)', () => {
    expect(classifyRealtimeEvent({ type: 'response.output_audio_transcript.delta', delta: 'Hi' }))
      .toEqual({ kind: 'assistant_delta', text: 'Hi' })
    expect(classifyRealtimeEvent({ type: 'response.audio_transcript.delta', delta: 'Hi' }))
      .toEqual({ kind: 'assistant_delta', text: 'Hi' })
  })

  it('maps assistant done with full transcript', () => {
    expect(classifyRealtimeEvent({ type: 'response.output_audio_transcript.done', transcript: 'Hi!' }))
      .toEqual({ kind: 'assistant_done', text: 'Hi!' })
  })

  it('extracts usage from response.done', () => {
    const r = classifyRealtimeEvent({
      type: 'response.done',
      response: { usage: { input_tokens: 12, output_tokens: 34 } },
    })
    expect(r.kind).toBe('usage')
    expect(r.usage.output_tokens).toBe(34)
  })

  it('ignores unknown and malformed events', () => {
    expect(classifyRealtimeEvent({ type: 'rate_limits.updated' }).kind).toBe('other')
    expect(classifyRealtimeEvent(null).kind).toBe('other')
    expect(classifyRealtimeEvent({}).kind).toBe('other')
  })
})

describe('usage accumulation', () => {
  it('adds usage events into a running total', () => {
    let t = addUsage(null, { input_tokens: 10, output_tokens: 5 })
    t = addUsage(t, { input_tokens: 2, output_tokens: 3 })
    expect(t).toEqual({ input: 12, output: 8 })
  })

  it('labels usage compactly and stays quiet at zero', () => {
    expect(voiceUsageLabel({ input: 12, output: 8 })).toBe('↑12 ↓8 tok')
    expect(voiceUsageLabel(null)).toBe('')
    expect(voiceUsageLabel({ input: 0, output: 0 })).toBe('')
  })
})

describe('voiceHint', () => {
  it('uses the server detail when unavailable', () => {
    expect(voiceHint('unavailable', 'no API key configured')).toBe('no API key configured')
    expect(voiceHint('unavailable')).toContain('config.yaml')
  })
  it('covers every state', () => {
    for (const s of ['idle', 'connecting', 'live', 'error']) {
      expect(voiceHint(s)).not.toBe('')
    }
  })
})
