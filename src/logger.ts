/**
 * Simple logger for the proxy
 */
export enum LogLevel {
  DEBUG = 0,
  INFO = 1,
  WARN = 2,
  ERROR = 3,
}

class Logger {
  private level: LogLevel;

  constructor(level: LogLevel = LogLevel.INFO) {
    this.level = level;
  }

  setLevel(level: LogLevel): void {
    this.level = level;
  }

  private formatMessage(level: string, message: string, ...args: unknown[]): string {
    const timestamp = new Date().toISOString();
    const formattedArgs = args.length > 0 
      ? ' ' + args.map(arg => {
          if (typeof arg === 'object') {
            try {
              return JSON.stringify(arg, null, 2);
            } catch {
              return String(arg);
            }
          }
          return String(arg);
        }).join(' ')
      : '';
    return `[${timestamp}] [${level}] ${message}${formattedArgs}`;
  }

  debug(message: string, ...args: unknown[]): void {
    if (this.level <= LogLevel.DEBUG) {
      console.debug(this.formatMessage('DEBUG', message, ...args));
    }
  }

  info(message: string, ...args: unknown[]): void {
    if (this.level <= LogLevel.INFO) {
      console.info(this.formatMessage('INFO', message, ...args));
    }
  }

  warn(message: string, ...args: unknown[]): void {
    if (this.level <= LogLevel.WARN) {
      console.warn(this.formatMessage('WARN', message, ...args));
    }
  }

  error(message: string, ...args: unknown[]): void {
    if (this.level <= LogLevel.ERROR) {
      console.error(this.formatMessage('ERROR', message, ...args));
    }
  }

  request(method: string, path: string, body?: unknown): void {
    if (this.level <= LogLevel.DEBUG) {
      console.debug(this.formatMessage('REQUEST', `${method} ${path}`, body ? body : ''));
    }
  }

  response(statusCode: number, body?: unknown): void {
    if (this.level <= LogLevel.DEBUG) {
      console.debug(this.formatMessage('RESPONSE', `Status: ${statusCode}`, body ? body : ''));
    }
  }
}

// Singleton instance
export const logger = new Logger(
  process.env.LOG_LEVEL === 'debug' ? LogLevel.DEBUG :
  process.env.LOG_LEVEL === 'warn' ? LogLevel.WARN :
  process.env.LOG_LEVEL === 'error' ? LogLevel.ERROR :
  LogLevel.INFO
);

export default logger;