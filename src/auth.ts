import axios from 'axios';
import { SapAiCoreCredentials, Token } from './types';
import { logger } from './logger';

/**
 * SAP AI Core Authentication Manager
 * Handles OAuth token acquisition and refresh
 */
export class AuthManager {
  private credentials: SapAiCoreCredentials;
  private token?: Token;

  constructor(credentials: SapAiCoreCredentials) {
    this.credentials = credentials;
  }

  /**
   * Validates that all required credentials are present
   */
  private validateCredentials(): void {
    if (!this.credentials.clientId || !this.credentials.clientSecret || 
        !this.credentials.tokenUrl || !this.credentials.baseUrl) {
      throw new Error('Missing required SAP AI Core credentials. Please check your configuration.');
    }
  }

  /**
   * Authenticates with SAP AI Core and retrieves an access token
   */
  private async authenticate(): Promise<Token> {
    this.validateCredentials();

    const tokenUrl = this.credentials.tokenUrl.replace(/\/+$/, '') + '/oauth/token';
    
    logger.debug(`Authenticating with SAP AI Core at ${tokenUrl}`);

    try {
      const response = await axios.post(
        tokenUrl,
        'grant_type=client_credentials&response_type=token',
        {
          headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
          },
          auth: {
            username: this.credentials.clientId,
            password: this.credentials.clientSecret,
          },
        }
      );

      const token: Token = {
        access_token: response.data.access_token,
        token_type: response.data.token_type || 'Bearer',
        expires_in: response.data.expires_in,
        expires_at: Date.now() + response.data.expires_in * 1000,
      };

      logger.info('Successfully authenticated with SAP AI Core');
      logger.debug(`Token expires at: ${new Date(token.expires_at).toISOString()}`);

      return token;
    } catch (error: unknown) {
      const axiosError = error as { response?: { status?: number; data?: unknown }; message?: string };
      logger.error('Authentication failed:', axiosError.response?.data || axiosError.message);
      throw new Error(`Failed to authenticate with SAP AI Core: ${axiosError.message || 'Unknown error'}`);
    }
  }

  /**
   * Gets a valid access token, refreshing if necessary
   */
  async getToken(): Promise<string> {
    // Check if token exists and is not expired (with 60 second buffer)
    if (this.token && this.token.expires_at > Date.now() + 60000) {
      return this.token.access_token;
    }

    logger.debug('Token expired or not present, refreshing...');
    this.token = await this.authenticate();
    return this.token.access_token;
  }

  /**
   * Forces a token refresh
   */
  async refreshToken(): Promise<string> {
    this.token = undefined;
    return this.getToken();
  }

  /**
   * Gets the base URL for SAP AI Core API
   */
  getBaseUrl(): string {
    return this.credentials.baseUrl;
  }

  /**
   * Gets the resource group
   */
  getResourceGroup(): string {
    return this.credentials.resourceGroup || 'default';
  }

  /**
   * Builds common headers for SAP AI Core API requests
   */
  async buildHeaders(): Promise<Record<string, string>> {
    const token = await this.getToken();
    return {
      'Authorization': `Bearer ${token}`,
      'AI-Resource-Group': this.getResourceGroup(),
      'Content-Type': 'application/json',
    };
  }
}