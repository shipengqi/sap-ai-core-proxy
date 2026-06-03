import type { Response } from 'express';
import { usesConverseApi } from '../model-catalogue';

type ClaudeHandler = (req: unknown, res: Response) => Promise<void>;

export class ClaudeDispatcher {
  constructor(
    private converseHandler: ClaudeHandler,
    private invokeHandler: ClaudeHandler
  ) {}

  dispatch(sapModelName: string, req: unknown, res: Response): Promise<void> {
    return usesConverseApi(sapModelName)
      ? this.converseHandler(req, res)
      : this.invokeHandler(req, res);
  }
}
