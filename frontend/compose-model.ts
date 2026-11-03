import {
  isAlias,
  isMap,
  isSeq,
  isScalar,
  parseDocument,
  stringify,
  type Node,
} from "yaml";

export type Path = (string | number)[];
export function parseCompose(text: string) {
  const doc = parseDocument(text, {
    merge: true,
    uniqueKeys: true,
    prettyErrors: false,
  });
  if (doc.errors.length) {
    const err = doc.errors[0];
    throw Error(
      (err.linePos?.[0]?.line ?? text.slice(0, err.pos[0]).split("\n").length) +
        ": " +
        err.message,
    );
  }
  const model = doc.toJS({ maxAliasCount: 100 });
  if (
    !model ||
    typeof model.services !== "object" ||
    !model.services ||
    Array.isArray(model.services)
  )
    throw Error("services: {}");
  return { doc, model };
}
type Parsed = ReturnType<typeof parseCompose>;
export function nodeAt(parsed: Parsed, path: Path) {
  return parsed.doc.getIn(path, true) as Node | undefined;
}
export function inherited(parsed: Parsed, path: Path) {
  return (
    !nodeAt(parsed, path) &&
    path.reduce((v, k) => v?.[k], parsed.model) !== undefined
  );
}
const compact = (v: unknown) => JSON.stringify(v);
const keyText = (key: string | number) =>
  /^[A-Za-z_][A-Za-z0-9_.-]*$/.test(String(key)) ? String(key) : compact(key);
export function patchCompose(text: string, path: Path, value: unknown): string {
  const parsed = parseCompose(text);
  const nl = text.includes("\r\n") ? "\r\n" : "\n";
  const startOfLine = (i: number) => text.lastIndexOf("\n", i - 1) + 1;
  function patch(keys: Path, next: unknown): string {
    const parentPath = keys.slice(0, -1),
      key = keys.at(-1)!;
    const parent = nodeAt(parsed, parentPath),
      node = nodeAt(parsed, keys);
    if (!parent || isAlias(parent) || isScalar(parent)) {
      if (!parentPath.length) throw Error("YAML");
      const existing = parentPath.reduce((v, k) => v?.[k], parsed.model);
      const copy = Array.isArray(existing)
        ? [...existing]
        : { ...(existing ?? {}) };
      if (next === undefined) delete copy[key];
      else copy[key] = next;
      if (isAlias(parent) && typeof key === "string" && next !== undefined) {
        const [a, b] = parent.range!;
        return (
          text.slice(0, a) +
          "{<<: *" +
          parent.source +
          ", " +
          keyText(key) +
          ": " +
          compact(next) +
          "}" +
          text.slice(b)
        );
      }
      return patch(parentPath, copy);
    }
    if (node && next !== undefined) {
      const [a, b] = node.range!;
      const old = text.slice(a, b);
      return (
        text.slice(0, a) +
        (text[a - 1] === ":" ? " " : "") +
        compact(next) +
        (old.endsWith("\n") ? nl : "") +
        text.slice(b)
      );
    }
    if (!isMap(parent) && !isSeq(parent)) throw Error("YAML");
    const items = parent.items;
    const fields = isMap(parent)
      ? parent.items.map((p) => ({
          key: p.key as Node,
          val: (p.value as Node) ?? (p.key as Node),
        }))
      : parent.items.map((p) => ({ key: p as Node, val: p as Node }));
    if (next === undefined) {
      const i = isMap(parent)
        ? parent.items.findIndex((p) => isScalar(p.key) && p.key.value === key)
        : Number(key);
      if (i < 0 || !fields[i]) return text;
      if (items.length === 1) return patch(parentPath, isSeq(parent) ? [] : {});
      const a = fields[i].key.range![0],
        b = fields[i].val.range![1];
      if (parent.flow) {
        const begin = i === 0 ? a : fields[i - 1].val.range![1],
          end = i === 0 ? fields[1].key.range![0] : b;
        return text.slice(0, begin) + text.slice(end);
      }
      const end = fields[i].val.range![2];
      return (
        text.slice(0, startOfLine(a)) +
        text.slice(
          text[end - 1] === "\n"
            ? end
            : text.indexOf("\n", end) + 1 || text.length,
        )
      );
    }
    const entry = isMap(parent)
      ? keyText(key) + ": " + compact(next)
      : compact(next);
    if (parent.flow) {
      const end = parent.range![1] - 1;
      const prefix = text.slice(0, end);
      return (
        prefix +
        (items.length && !prefix.trimEnd().endsWith(",") ? ", " : "") +
        entry +
        text.slice(end)
      );
    }
    const end = parent.range![1];
    const indent = parent.range![0] - startOfLine(parent.range![0]);
    const block = stringify(isMap(parent) ? { [key]: next } : [next], {
      lineWidth: 0,
    })
      .trimEnd()
      .split("\n")
      .map((line) => " ".repeat(indent) + line)
      .join(nl);
    return (
      text.slice(0, end) +
      (text[end - 1] === "\n" ? "" : nl) +
      block +
      nl +
      text.slice(end)
    );
  }
  // Editing an anchor definition would also change its other consumers.
  for (let i = 1; i <= path.length; i++) {
    const n = nodeAt(parsed, path.slice(0, i));
    if (n && "anchor" in n && n.anchor) throw Error("&" + n.anchor);
  }
  const result = patch(path, value);
  parseCompose(result);
  return result;
}
