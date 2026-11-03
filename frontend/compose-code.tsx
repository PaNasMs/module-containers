import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import { EditorView } from "@codemirror/view";
import { yaml } from "@codemirror/lang-yaml";
import { Compartment } from "@codemirror/state";
import { oneDark } from "@codemirror/theme-one-dark";
export function ComposeCode({
  text,
  change,
}: {
  text: string;
  change: (s: string) => void;
}) {
  const root = useRef<HTMLDivElement>(null),
    view = useRef<EditorView | null>(null),
    notify = useRef(change);
  notify.current = change;
  useEffect(() => {
    const theme = new Compartment(),
      appearance = () =>
        document.documentElement.dataset.theme?.includes("dark") ? oneDark : [];
    const v = new EditorView({
      parent: root.current!,
      doc: text,
      extensions: [
        basicSetup,
        yaml(),
        theme.of(appearance()),
        EditorView.lineWrapping,
        EditorView.contentAttributes.of({
          "aria-label": "Compose YAML",
          role: "textbox",
          "aria-multiline": "true",
        }),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) notify.current(u.state.doc.toString());
        }),
      ],
    });
    view.current = v;
    const observer = new MutationObserver(() =>
      v.dispatch({ effects: theme.reconfigure(appearance()) }),
    );
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    return () => {
      observer.disconnect();
      v.destroy();
      view.current = null;
    };
  }, []);
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== text)
      v.dispatch({
        changes: { from: 0, to: v.state.doc.length, insert: text },
      });
  }, [text]);
  return <div className="compose-code" ref={root} />;
}
