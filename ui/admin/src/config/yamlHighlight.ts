export type YAMLToken = {
  text: string;
  kind: "key" | "string" | "comment" | "value" | "space";
};

export function tokenizeYAML(yaml: string): YAMLToken[][] {
  return yaml.split(/\r?\n/).map(tokenizeLine);
}

function tokenizeLine(line: string): YAMLToken[] {
  const tokens: YAMLToken[] = [];
  let cursor = 0;

  const leading = line.match(/^\s+/)?.[0] ?? "";
  if (leading) {
    tokens.push({ text: leading, kind: "space" });
    cursor = leading.length;
  }

  const commentIndex = findCommentIndex(line, cursor);
  const contentEnd = commentIndex >= 0 ? commentIndex : line.length;
  const content = line.slice(cursor, contentEnd);
  const keyMatch = content.match(/^([^:[\]{}#]+:)/);

  if (keyMatch) {
    tokens.push({ text: keyMatch[1], kind: "key" });
    cursor += keyMatch[1].length;
  }

  while (cursor < contentEnd) {
    const char = line[cursor];
    if (/\s/.test(char)) {
      const start = cursor;
      while (cursor < contentEnd && /\s/.test(line[cursor])) cursor++;
      tokens.push({ text: line.slice(start, cursor), kind: "space" });
      continue;
    }
    if (char === '"' || char === "'") {
      const quote = char;
      const start = cursor;
      cursor++;
      while (cursor < contentEnd && line[cursor] !== quote) cursor++;
      cursor = Math.min(cursor + 1, contentEnd);
      tokens.push({ text: line.slice(start, cursor), kind: "string" });
      continue;
    }
    const start = cursor;
    while (cursor < contentEnd && !/\s/.test(line[cursor])) cursor++;
    tokens.push({ text: line.slice(start, cursor), kind: "value" });
  }

  if (commentIndex >= 0) {
    if (commentIndex > 0 && line[commentIndex - 1] === " ") {
      const previous = tokens[tokens.length - 1];
      if (previous?.kind !== "space") {
        tokens.push({ text: " ", kind: "space" });
      }
    }
    tokens.push({ text: line.slice(commentIndex), kind: "comment" });
  }

  return tokens;
}

function findCommentIndex(line: string, start: number): number {
  let quote: string | null = null;
  for (let index = start; index < line.length; index++) {
    const char = line[index];
    if ((char === '"' || char === "'") && line[index - 1] !== "\\") {
      quote = quote === char ? null : quote ?? char;
    }
    if (char === "#" && !quote && (index === 0 || /\s/.test(line[index - 1]))) {
      return index;
    }
  }
  return -1;
}
