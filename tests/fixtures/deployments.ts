import type { DeploymentsResponse } from '../../src/sap-ai-core/types';

function makeDeployment(id: string, modelName: string) {
  return {
    id,
    configurationId: `config-${id}`,
    configurationName: `test-${modelName}`,
    scenarioId: 'foundation-models',
    status: 'RUNNING',
    targetStatus: 'RUNNING',
    createdAt: '2024-01-01T00:00:00Z',
    modifiedAt: '2024-01-01T00:00:00Z',
    submissionTime: '2024-01-01T00:00:00Z',
    startTime: '2024-01-01T00:00:00Z',
    deploymentUrl: `https://api.ai.test.example.com/v2/inference/deployments/${id}`,
    details: {
      resources: {
        backend_details: {
          model: { name: modelName, version: '1' },
        },
      },
    },
  };
}

export const DEPLOY_OPENAI_ID = 'deploy-openai-001';
export const DEPLOY_CLAUDE_ID = 'deploy-claude-001';
export const DEPLOY_GEMINI_ID = 'deploy-gemini-001';

export const DEPLOYMENTS_RESPONSE: DeploymentsResponse = {
  count: 3,
  resources: [
    makeDeployment(DEPLOY_OPENAI_ID, 'gpt-4o'),
    makeDeployment(DEPLOY_CLAUDE_ID, 'anthropic--claude-4.5-sonnet'),
    makeDeployment(DEPLOY_GEMINI_ID, 'gemini-2.5-flash'),
  ],
};
