import { describe, it, expect } from 'vitest';
import {
  getProvider,
  mapFromAnthropic,
  usesConverseApi,
  getOwner,
  tryGetEntry,
  getEntry,
} from '../src/model-catalogue';

describe('model-catalogue', () => {
  describe('getProvider', () => {
    it.each([
      ['gpt-4o',                              'openai'],
      ['anthropic--claude-4.5-sonnet',        'anthropic'],
      ['gemini-2.5-flash',                    'gemini'],
      ['meta--llama3-70b-instruct',           'meta'],
      ['mistralai--mixtral-8x7b-instruct-v01','mistral'],
    ])('%s → %s', (model, expected) => {
      expect(getProvider(model)).toBe(expected);
    });

    it('throws for unknown model', () => {
      expect(() => getProvider('unknown-model-xyz')).toThrow('Unknown model');
    });
  });

  describe('mapFromAnthropic', () => {
    it('resolves Anthropic alias to SAP name', () => {
      expect(mapFromAnthropic('claude-sonnet-4-5')).toBe('anthropic--claude-4.5-sonnet');
    });

    it('resolves versioned alias', () => {
      expect(mapFromAnthropic('claude-3-5-sonnet-20241022')).toBe('anthropic--claude-3.5-sonnet');
    });

    it('passes through valid SAP name containing "--"', () => {
      expect(mapFromAnthropic('anthropic--claude-4.5-sonnet')).toBe('anthropic--claude-4.5-sonnet');
    });

    it('throws for unknown Anthropic alias', () => {
      expect(() => mapFromAnthropic('claude-unknown-99')).toThrow('Unknown Anthropic model');
    });

    it('throws for invalid SAP name containing "--"', () => {
      expect(() => mapFromAnthropic('unknown--model')).toThrow('Unknown model');
    });
  });

  describe('usesConverseApi', () => {
    it('returns true for Claude 4.5+ (Converse path)', () => {
      expect(usesConverseApi('anthropic--claude-4.5-sonnet')).toBe(true);
    });

    it('returns false for Claude 3 (Invoke path)', () => {
      expect(usesConverseApi('anthropic--claude-3-opus')).toBe(false);
    });

    it('returns false for OpenAI models', () => {
      expect(usesConverseApi('gpt-4o')).toBe(false);
    });
  });

  describe('getOwner', () => {
    it('returns "google" for Gemini models', () => {
      expect(getOwner('gemini-2.5-flash')).toBe('google');
    });

    it('returns "openai" for OpenAI models', () => {
      expect(getOwner('gpt-4o')).toBe('openai');
    });

    it('returns "anthropic" for Claude models', () => {
      expect(getOwner('anthropic--claude-4.5-sonnet')).toBe('anthropic');
    });

    it('returns "sap-ai-core" for unknown models', () => {
      expect(getOwner('unknown-model')).toBe('sap-ai-core');
    });
  });

  describe('tryGetEntry', () => {
    it('returns entry for known model', () => {
      const entry = tryGetEntry('gpt-4o');
      expect(entry).toBeDefined();
      expect(entry?.provider).toBe('openai');
    });

    it('returns undefined for unknown model', () => {
      expect(tryGetEntry('not-a-real-model')).toBeUndefined();
    });
  });

  describe('getEntry', () => {
    it('returns full entry for known model', () => {
      const entry = getEntry('anthropic--claude-4.5-sonnet');
      expect(entry.sapName).toBe('anthropic--claude-4.5-sonnet');
      expect(entry.usesConverseApi).toBe(true);
      expect(entry.provider).toBe('anthropic');
    });

    it('throws for unknown model', () => {
      expect(() => getEntry('fake-model')).toThrow('Unknown model');
    });
  });
});
