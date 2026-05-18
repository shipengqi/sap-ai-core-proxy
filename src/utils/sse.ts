import { Response } from 'express';
import { logger } from '../logger';

/**
 * Sets standard Server-Sent Events headers on the response.
 */
export function setSSEHeaders(res: Response): void {
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-cache');
  res.setHeader('Connection', 'keep-alive');
  res.setHeader('X-Accel-Buffering', 'no');
}

/**
 * Sends a single SSE event with the given event type and data payload.
 */
export function sendSSEEvent(res: Response, eventType: string, data: unknown): void {
  res.write(`event: ${eventType}\ndata: ${JSON.stringify(data)}\n\n`);
}

/**
 * Terminates a stream cleanly on error. If headers haven't been sent yet,
 * returns a 500 JSON error. Otherwise writes [DONE] so the client knows
 * the stream ended rather than receiving a silent truncation.
 */
export function endStreamOnError(res: Response, error: Error): void {
  logger.error('Stream error:', error.message);
  if (!res.headersSent) {
    res.status(500).json({ error: { message: error.message, type: 'api_error', param: null, code: '500' } });
  } else {
    res.write('data: [DONE]\n\n');
    res.end();
  }
}
