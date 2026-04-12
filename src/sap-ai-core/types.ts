// SAP AI Core Types
export interface SapAiCoreCredentials {
  clientId: string;
  clientSecret: string;
  tokenUrl: string;
  baseUrl: string;
  resourceGroup?: string;
}

export interface Token {
  access_token: string;
  token_type: string;
  expires_in: number;
  expires_at: number;
}

export interface Deployment {
  id: string;
  configurationId: string;
  configurationName: string;
  scenarioId: string;
  status: string;
  targetStatus: string;
  createdAt: string;
  modifiedAt: string;
  submissionTime: string;
  startTime: string;
  deploymentUrl: string;
  details: {
    resources: {
      backend_details: {
        model: {
          name: string;
          version: string;
        };
      };
    };
    scaling?: {
      backend_details: Record<string, unknown>;
    };
  };
}

export interface DeploymentsResponse {
  count: number;
  resources: Deployment[];
}
