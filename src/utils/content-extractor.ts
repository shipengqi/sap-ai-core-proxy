/**
 * Extracts text content from OpenAI message content field.
 * Handles both string and array formats.
 */
export function extractTextContent(
  content: string | null | undefined | Array<{ type: string; text?: string }>
): string {
  if (!content) {
    return '';
  }
  if (typeof content === 'string') {
    return content;
  }
  if (Array.isArray(content)) {
    return content
      .filter((item) => item.type === 'text' && item.text)
      .map((item) => item.text)
      .join('');
  }
  return String(content);
}
