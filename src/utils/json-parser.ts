/**
 * Converts Python-style JSON (single quotes) to standard JSON (double quotes).
 * SAP AI Core may return single-quoted JSON in streaming responses.
 */
export function convertPythonJsonToStandardJson(jsonStr: string): string {
  let result = '';
  let inString = false;
  let stringChar = '';

  for (let i = 0; i < jsonStr.length; i++) {
    const char = jsonStr[i];
    const prevChar = i > 0 ? jsonStr[i - 1] : '';

    if (!inString) {
      if (char === "'" || char === '"') {
        inString = true;
        stringChar = char;
        result += '"';
      } else {
        result += char;
      }
    } else {
      if (char === stringChar && prevChar !== '\\') {
        inString = false;
        result += '"';
      } else if (char === '"' && stringChar === "'") {
        result += '\\"';
      } else {
        result += char;
      }
    }
  }

  return result;
}
