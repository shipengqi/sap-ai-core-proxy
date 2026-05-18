import axios, { AxiosResponse } from 'axios';
import { AuthManager } from './auth';

const INFERENCE_TIMEOUT_MS = 30_000;
const API_TIMEOUT_MS = 10_000;

export class SapClient {
  constructor(private authManager: AuthManager) {}

  async post(path: string, body: unknown): Promise<AxiosResponse> {
    const { headers, url } = await this.prepare(path);
    return axios.post(url, body, { headers, timeout: INFERENCE_TIMEOUT_MS });
  }

  async postStream(path: string, body: unknown): Promise<AxiosResponse> {
    const { headers, url } = await this.prepare(path);
    return axios.post(url, body, {
      headers,
      responseType: 'stream',
      validateStatus: (s) => s < 500,
      timeout: INFERENCE_TIMEOUT_MS,
    });
  }

  async get<T = unknown>(path: string): Promise<AxiosResponse<T>> {
    const { headers, url } = await this.prepare(path);
    return axios.get<T>(url, { headers, timeout: API_TIMEOUT_MS });
  }

  async delete(path: string): Promise<AxiosResponse> {
    const { headers, url } = await this.prepare(path);
    return axios.delete(url, { headers, timeout: API_TIMEOUT_MS });
  }

  async postForm(path: string, form: FormData): Promise<AxiosResponse> {
    const { headers, url } = await this.prepare(path);
    const { 'Content-Type': _, ...headersWithoutContentType } = headers;
    return axios.post(url, form, { headers: headersWithoutContentType, timeout: INFERENCE_TIMEOUT_MS });
  }

  private async prepare(path: string): Promise<{ headers: Record<string, string>; url: string }> {
    const headers = await this.authManager.buildHeaders();
    const url = `${this.authManager.getBaseUrl()}${path}`;
    return { headers, url };
  }
}
