// Editor setup.
//
// CodeMirror is assembled by hand rather than through basicSetup, because
// basicSetup pulls in autocompletion and linting - exactly the help this
// trainer exists to remove. @codemirror/autocomplete is not imported anywhere,
// so there is no completion source to accidentally switch back on.
//
// If the CDN is unreachable the editor falls back to a plain textarea. Losing
// syntax highlighting is annoying; losing the ability to practise offline
// would be worse.

const TAB = "\t";

let modules = null;
let loadFailed = false;

async function load() {
  if (modules || loadFailed) return modules;
  try {
    const [view, state, language, commands, search, goMode] = await Promise.all([
      import("@codemirror/view"),
      import("@codemirror/state"),
      import("@codemirror/language"),
      import("@codemirror/commands"),
      import("@codemirror/search"),
      import("@codemirror/legacy-modes/mode/go"),
    ]);
    modules = { view, state, language, commands, search, goMode };
  } catch (err) {
    console.warn("CodeMirror не загрузился, работаем в простом редакторе:", err);
    loadFailed = true;
  }
  return modules;
}

/**
 * Creates an editor inside host.
 * Returns { getValue, setValue, focus, destroy, kind }.
 */
export async function createEditor(host, doc, { onChange, onActivity, onRun } = {}) {
  const m = await load();
  if (!m) return plainEditor(host, doc, { onChange, onActivity, onRun });

  const { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, rectangularSelection, crosshairCursor, highlightSpecialChars } = m.view;
  const { EditorState, Compartment } = m.state;
  const { StreamLanguage, syntaxHighlighting, defaultHighlightStyle, bracketMatching, indentUnit } = m.language;
  const { defaultKeymap, history, historyKeymap, indentWithTab } = m.commands;
  const { searchKeymap, highlightSelectionMatches } = m.search;

  const runKeymap = keymap.of([
    {
      key: "Mod-Enter",
      preventDefault: true,
      run: () => {
        onRun?.();
        return true;
      },
    },
  ]);

  const view = new EditorView({
    parent: host,
    state: EditorState.create({
      doc,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightActiveLine(),
        highlightSpecialChars(),
        history(),
        drawSelection(),
        rectangularSelection(),
        crosshairCursor(),
        bracketMatching(),
        highlightSelectionMatches(),
        EditorState.allowMultipleSelections.of(true),
        // Go is written with tabs, and gofmt is not run for you.
        indentUnit.of(TAB),
        EditorState.tabSize.of(4),
        StreamLanguage.define(m.goMode.go),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        runKeymap,
        keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            onChange?.(update.state.doc.toString());
            onActivity?.();
          } else if (update.selectionSet) {
            onActivity?.();
          }
        }),
      ],
    }),
  });
  void Compartment;

  return {
    kind: "codemirror",
    getValue: () => view.state.doc.toString(),
    setValue(next) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: next } });
    },
    focus: () => view.focus(),
    destroy: () => view.destroy(),
  };
}

function plainEditor(host, doc, { onChange, onActivity, onRun } = {}) {
  const area = document.createElement("textarea");
  area.className = "plain-editor";
  area.spellcheck = false;
  area.autocapitalize = "off";
  area.autocomplete = "off";
  area.setAttribute("autocorrect", "off");
  area.value = doc;
  host.append(area);

  area.addEventListener("input", () => {
    onChange?.(area.value);
    onActivity?.();
  });
  area.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      onRun?.();
      return;
    }
    if (e.key === "Tab") {
      // A textarea would move focus; Go wants a tab.
      e.preventDefault();
      const { selectionStart: from, selectionEnd: to } = area;
      area.value = area.value.slice(0, from) + TAB + area.value.slice(to);
      area.selectionStart = area.selectionEnd = from + TAB.length;
      onChange?.(area.value);
      onActivity?.();
    }
  });

  return {
    kind: "textarea",
    getValue: () => area.value,
    setValue(next) {
      area.value = next;
    },
    focus: () => area.focus(),
    destroy: () => area.remove(),
  };
}
