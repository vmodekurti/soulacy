// @vitest-environment jsdom
//
// Video support in the shared markdown renderer: direct video-file links become
// <video> players, YouTube/Vimeo links become whitelisted <iframe> embeds, and
// any non-whitelisted iframe an agent tries to inject is dropped.

import { describe, it, expect } from 'vitest'
import { parseMarkdown } from './markdown.js'

describe('parseMarkdown video support', () => {
  it('turns a bare direct video link into a <video> player', () => {
    const html = parseMarkdown('https://example.com/clip.mp4')
    expect(html).toContain('<video')
    expect(html).toContain('controls')
    expect(html).toContain('src="https://example.com/clip.mp4"')
    expect(html).not.toContain('<iframe')
  })

  it('handles a video URL with query/hash', () => {
    const html = parseMarkdown('https://cdn.example.com/a/b.webm?token=xyz#t=10')
    expect(html).toContain('<video')
    expect(html).toContain('b.webm?token=xyz#t=10')
  })

  it('embeds a YouTube watch link as a whitelisted iframe', () => {
    const html = parseMarkdown('https://www.youtube.com/watch?v=dQw4w9WgXcQ')
    expect(html).toContain('<iframe')
    expect(html).toContain('https://www.youtube.com/embed/dQw4w9WgXcQ')
  })

  it('embeds a youtu.be short link', () => {
    const html = parseMarkdown('https://youtu.be/dQw4w9WgXcQ')
    expect(html).toContain('https://www.youtube.com/embed/dQw4w9WgXcQ')
  })

  it('embeds a Vimeo link', () => {
    const html = parseMarkdown('https://vimeo.com/123456789')
    expect(html).toContain('https://player.vimeo.com/video/123456789')
  })

  it('does not convert a normal (non-video) link', () => {
    const html = parseMarkdown('https://example.com/article')
    expect(html).not.toContain('<video')
    expect(html).not.toContain('<iframe')
    expect(html).toContain('<a')
  })

  it('does not eat a link with custom text', () => {
    const html = parseMarkdown('[watch here](https://example.com/clip.mp4)')
    expect(html).not.toContain('<video')
    expect(html).toContain('watch here')
  })

  it('drops a raw iframe pointing at an untrusted host', () => {
    const html = parseMarkdown('<iframe src="https://evil.example.com/x"></iframe>')
    expect(html).not.toContain('evil.example.com')
  })
})
