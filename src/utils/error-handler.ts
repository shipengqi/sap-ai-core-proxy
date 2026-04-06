import { Response } from 'express';
import { logger } from '../logger';

interface ErrorDetails {
  readonly statusCode: number;
  readonly message: string;
}

/**
 * Extracts status code and error message from an unknown error (typically an Axios error).
 * Handles SAP AI Core error formats: { errors: { message } }, { error: { message } }, { message }.
 */
export function extractErrorDetails(error: unknown): ErrorDetails {
  const axiosError = error as {
    response?: { status?: number; data?: unknown; headers?: Record<string, string> };
    message?: string;
    config?: { url?: string; method?: string };
  };

  logger.error('Request error:', {
    message: axiosError.message,
    status: axiosError.response?.status,
    url: axiosError.config?.url,
    method: axiosError.config?.method,
    responseData: axiosError.response?.data,
  });

  const statusCode = axiosError.response?.status || 500;
  let message = 'Internal server error';
  const responseData = axiosError.response?.data;

  if (responseData) {
    if (typeof responseData === 'string') {
      message = responseData;
    } else if (typeof responseData === 'object') {
      const data = responseData as Record<string, unknown>;
      if (data.errors && typeof data.errors === 'object') {
        const errors = data.errors as Record<string, unknown>;
        message = (errors.message as string) || JSON.stringify(data.errors);
      } else if (data.error && typeof data.error === 'object') {
        const err = data.error as Record<string, unknown>;
        message = (err.message as string) || JSON.stringify(data.error);
      } else if (data.message) {
        message = data.message as string;
      } else {
        message = JSON.stringify(responseData);
      }
    }
  } else if (axiosError.message) {
    message = axiosError.message;
  }

  return { statusCode, message };
}

/**
 * Sends an error response in OpenAI API format.
 */
export function sendOpenAIError(
  res: Response,
  statusCode: number,
  message: string,
  type: string = 'api_error'
): void {
  res.status(statusCode).json({
    error: {
      message,
      type,
      param: null,
      code: statusCode.toString(),
    },
  });
}

/**
 * Sends an error response in Anthropic API format.
 */
export function sendAnthropicError(
  res: Response,
  statusCode: number,
  message: string
): void {
  res.status(statusCode).json({
    type: 'error',
    error: {
      type: 'api_error',
      message,
    },
  });
}
