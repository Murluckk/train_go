// The task screen: statement on the left, editor and test output on the right.

import { el, clear, mount, api, fmtClock, fmtShort, fmtDay, markdown, setStatus, errorBox } from "/util.js";
import { createEditor } from "/editor.js";

// The heartbeat only counts time the tab is visible and recently typed in.
// Wall clock alone would count the coffee break, and the chart would lie.
const TICK_MS = 5000;
const FLUSH_MS = 15000;
const IDLE_MS = 90000;

export async function renderTaskScreen(root, taskID) {
  const task = await api(`/api/tasks/${encodeURIComponent(taskID)}`);
  const attempt = await api(`/api/tasks/${encodeURIComponent(taskID)}/attempts`, { method: "POST" });

  const screen = new TaskScreen(root, task, attempt);
  screen.mount();
  return () => screen.dispose();
}

class TaskScreen {
  constructor(root, task, attempt) {
    this.root = root;
    this.task = task;
    this.attempt = attempt;
    this.files = new Map();
    this.editors = new Map();
    this.activePath = null;
    this.stream = null;
    this.timers = [];
    this.pendingActiveMs = 0;
    this.lastInputAt = Date.now();
    this.disposed = false;
    this.solved = attempt.status === "passed";

    for (const f of task.starter) {
      this.files.set(f.path, task.draft?.[f.path] ?? f.content);
    }
    // A draft may hold a file the starter no longer has.
    for (const [path, content] of Object.entries(task.draft || {})) {
      if (!this.files.has(path)) this.files.set(path, content);
    }
    this.activePath = this.files.keys().next().value;
  }

  mount() {
    const t = this.task;

    this.timerNode = el("span", { class: "timer" }, "00:00");
    this.activeNode = el("span", { class: "timer idle" }, "00:00");
    this.consoleNode = el("div", { class: "console" }, "Тесты ещё не запускались.");
    this.verdictNode = el("div", {});
    this.reviewNode = el("div", {});
    this.editorHost = el("div", { class: "editor-host" });
    this.tabsNode = el("div", { class: "tabs" });

    this.runButton = el("button", { class: "primary", onclick: () => this.run() }, "Запустить тесты");
    this.cancelButton = el("button", { onclick: () => this.cancel(), disabled: true }, "Остановить");

    const header = el("div", {},
      el("h1", {}, t.title),
      el("p", { class: "sub" },
        `${t.topic} · сложность ${t.difficulty} · ~${t.estimate_minutes} мин` +
        (t.race ? " · тесты с -race" : " · без -race") +
        (t.passes ? ` · решена ${t.passes}, лучшее ${fmtShort(t.best_active_ms)}` : "")),
    );

    if (t.changed) {
      mount(header, el("div", { class: "error-box" },
        "Задача изменилась с тех пор, как ты её решал. Условие или тесты уже другие."));
    }

    const left = el("div", { class: "panel" },
      el("div", { class: "panel-head" }, "Условие"),
      el("div", { class: "panel-body readme", html: markdown(t.readme) }),
    );

    const right = el("div", {},
      el("div", { class: "panel" },
        el("div", { class: "panel-head" },
          this.tabsNode,
          el("span", { class: "spacer", style: "flex:1" }),
          el("span", {}, "в работе "), this.timerNode,
          el("span", {}, " · за клавиатурой "), this.activeNode,
        ),
        this.editorHost,
      ),
      el("div", { class: "actions" },
        this.runButton,
        this.cancelButton,
        el("span", { class: "muted" }, "Ctrl/Cmd + Enter"),
        el("span", { style: "flex:1" }),
        el("button", { onclick: () => this.restart() }, "Начать заново"),
      ),
      this.verdictNode,
      this.consoleNode,
      this.reviewNode,
    );

    mount(this.root, header, el("div", { class: "task-layout" }, left, right));

    this.buildTabs();
    this.openFile(this.activePath);
    this.startClocks();
    this.trackActivity();

    if (this.solved) this.showReview();
  }

  buildTabs() {
    mount(clear(this.tabsNode),
      [...this.files.keys()].map((path) =>
        el("button", {
          class: path === this.activePath ? "active" : "",
          onclick: () => this.openFile(path),
          dataset: { path },
        }, path),
      ),
    );
  }

  async openFile(path) {
    if (!this.files.has(path)) return;

    const current = this.editors.get(this.activePath);
    if (current) this.files.set(this.activePath, current.getValue());

    this.activePath = path;
    for (const b of this.tabsNode.children) b.classList.toggle("active", b.dataset.path === path);

    for (const ed of this.editors.values()) ed.destroy();
    this.editors.clear();
    clear(this.editorHost);

    const editor = await createEditor(this.editorHost, this.files.get(path), {
      onChange: (value) => {
        this.files.set(path, value);
        this.scheduleSave();
      },
      onActivity: () => {
        this.lastInputAt = Date.now();
      },
      onRun: () => this.run(),
    });
    if (this.disposed) {
      editor.destroy();
      return;
    }
    this.editors.set(path, editor);
    editor.focus();
  }

  currentFiles() {
    const editor = this.editors.get(this.activePath);
    if (editor) this.files.set(this.activePath, editor.getValue());
    return Object.fromEntries(this.files);
  }

  // ------------------------------------------------------------ clocks

  startClocks() {
    // Anchor on the server's elapsed time so a skewed browser clock cannot
    // shift the measurement.
    this.startedAtLocal = Date.now() - (this.attempt.elapsed_ms || 0);
    this.activeMs = this.attempt.active_ms || 0;

    const tick = () => {
      if (this.solved) return;
      this.timerNode.textContent = fmtClock(Date.now() - this.startedAtLocal);

      const idle = Date.now() - this.lastInputAt > IDLE_MS || document.visibilityState !== "visible";
      this.activeNode.classList.toggle("idle", idle);
      if (!idle) {
        this.activeMs += TICK_MS;
        this.pendingActiveMs += TICK_MS;
      }
      this.activeNode.textContent = fmtClock(this.activeMs);
    };

    this.timerNode.textContent = fmtClock(Date.now() - this.startedAtLocal);
    this.activeNode.textContent = fmtClock(this.activeMs);
    this.timers.push(setInterval(tick, TICK_MS));
    this.timers.push(setInterval(() => this.flushActivity(), FLUSH_MS));
  }

  async flushActivity() {
    if (this.solved || this.pendingActiveMs <= 0) return;
    const ms = this.pendingActiveMs;
    this.pendingActiveMs = 0;
    try {
      await api(`/api/attempts/${this.attempt.id}/heartbeat`, { method: "POST", body: { active_ms: ms } });
    } catch {
      // Losing one heartbeat is not worth an error box; put it back so the
      // next flush carries it.
      this.pendingActiveMs += ms;
    }
  }

  trackActivity() {
    this.onVisibility = () => {
      if (document.visibilityState === "visible") this.lastInputAt = Date.now();
    };
    document.addEventListener("visibilitychange", this.onVisibility);
  }

  scheduleSave() {
    clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => this.save(), 1200);
  }

  async save() {
    if (this.disposed) return;
    try {
      await api(`/api/attempts/${this.attempt.id}/draft`, {
        method: "PUT",
        body: { files: this.currentFiles() },
      });
    } catch (err) {
      console.warn("не удалось сохранить черновик", err);
    }
  }

  // ------------------------------------------------------------ running

  async run() {
    if (this.running) return;
    this.running = true;
    this.runButton.disabled = true;
    this.cancelButton.disabled = false;
    clear(this.verdictNode);
    clear(this.reviewNode);
    clear(this.consoleNode);
    setStatus("прогон…");

    await this.flushActivity();

    let started;
    try {
      started = await api("/api/runs", {
        method: "POST",
        body: { attempt_id: this.attempt.id, files: this.currentFiles() },
      });
    } catch (err) {
      this.verdictNode.append(errorBox(err));
      this.finishRun();
      return;
    }

    this.runID = started.run_id;
    this.follow(started.run_id);
  }

  follow(runID) {
    const source = new EventSource(`/api/runs/${runID}/events`);
    this.stream = source;

    source.addEventListener("log", (e) => {
      const { stream, text } = JSON.parse(e.data);
      this.appendLog(text, stream);
    });

    source.addEventListener("done", (e) => {
      const { result } = JSON.parse(e.data);
      source.close();
      this.stream = null;
      this.showVerdict(result);
      this.finishRun();
      if (result.outcome === "passed") this.onSolved();
    });

    source.addEventListener("closing", () => {
      source.close();
      this.stream = null;
      this.appendLog("\nсервер останавливается\n", "harness");
      this.finishRun();
    });

    source.onerror = () => {
      // EventSource retries by itself; Last-Event-ID replays the tail. Only
      // give up once the connection is definitively closed.
      if (source.readyState === EventSource.CLOSED) {
        this.stream = null;
        this.appendLog("\nсоединение с потоком прервалось\n", "harness");
        this.finishRun();
      }
    };
  }

  appendLog(text, stream) {
    if (!text) return;
    const atBottom = this.consoleNode.scrollTop + this.consoleNode.clientHeight >= this.consoleNode.scrollHeight - 24;
    mount(this.consoleNode, el("span", { class: stream || "" }, text));
    if (atBottom) this.consoleNode.scrollTop = this.consoleNode.scrollHeight;
  }

  finishRun() {
    this.running = false;
    this.runButton.disabled = false;
    this.cancelButton.disabled = true;
    setStatus("");
  }

  async cancel() {
    if (!this.runID) return;
    this.cancelButton.disabled = true;
    try {
      await api(`/api/runs/${this.runID}/cancel`, { method: "POST" });
    } catch (err) {
      console.warn("не удалось остановить прогон", err);
    }
  }

  showVerdict(result) {
    const titles = {
      passed: "Зелёно",
      failed: "Тесты не прошли",
      build_failed: "Не компилируется",
      timeout: "Таймаут",
      canceled: "Остановлено",
      error: "Ошибка раннера",
    };
    const parts = [];
    if (result.passed) parts.push(`прошло ${result.passed}`);
    if (result.failed) parts.push(`упало ${result.failed}`);
    if (result.skipped) parts.push(`пропущено ${result.skipped}`);
    parts.push(`${(result.duration_ms / 1000).toFixed(1)}с`);

    mount(clear(this.verdictNode),
      el("div", { class: `verdict ${result.outcome}` },
        el("span", {}, titles[result.outcome] || result.outcome),
        el("small", {}, parts.join(" · ")),
        result.failed_tests?.length
          ? el("small", { class: "mono" }, result.failed_tests.join(", "))
          : null,
      ),
      result.message ? el("pre", { class: "console" }, result.message) : null,
    );
  }

  async onSolved() {
    this.solved = true;
    await this.flushActivity();
    // The server records the solve after the stream closes, so poll briefly
    // rather than guess.
    for (let i = 0; i < 40; i++) {
      const fresh = await api(`/api/attempts/${this.attempt.id}`);
      if (fresh.status === "passed") {
        this.attempt = fresh;
        this.timerNode.textContent = fmtClock(fresh.duration_ms);
        this.activeNode.textContent = fmtClock(fresh.active_ms);
        this.activeNode.classList.remove("idle");
        await this.showReview();
        return;
      }
      await new Promise((r) => setTimeout(r, 150));
    }
  }

  async showReview() {
    let review;
    try {
      review = await api(`/api/attempts/${this.attempt.id}/review`);
    } catch (err) {
      mount(clear(this.reviewNode), errorBox(err));
      return;
    }

    const noteArea = el("textarea", {
      class: "note-input",
      placeholder: "Что я сделал криво? Что пришлось вспоминать? Где полез бы за подсказкой?",
    });
    noteArea.value = review.note || "";

    const saved = el("span", { class: "muted" }, review.note ? "заметка сохранена" : "");

    mount(clear(this.reviewNode),
      el("h2", {}, "Разбор"),
      el("p", { class: "sub" },
        review.next_due_on
          ? `Следующий повтор ${fmtDay(review.next_due_on)} (через ${review.next_in_days} дн.)`
          : "Повтор не запланирован"),

      el("h3", {}, "Мой код и эталон"),
      review.diffs.map((d) => renderDiff(d, "мой код", "эталон")),

      review.gofmt_diffs?.length
        ? [
            el("h3", {}, "Что сделал бы gofmt"),
            el("p", { class: "sub" },
              "Форматирование руками — часть тренировки, поэтому редактор не правит его за тебя."),
            review.gofmt_diffs.map((d) => renderDiff(d, "как написал", "как отформатировал бы gofmt")),
          ]
        : null,

      el("h3", {}, "Журнал расхождений"),
      el("div", { class: "note-form" }, noteArea),
      el("div", { class: "actions" },
        el("button", {
          class: "primary",
          onclick: async (e) => {
            e.target.disabled = true;
            try {
              await api("/api/notes", {
                method: "POST",
                body: { attempt_id: this.attempt.id, body: noteArea.value },
              });
              saved.textContent = "заметка сохранена";
            } catch (err) {
              saved.textContent = err.message;
            } finally {
              e.target.disabled = false;
            }
          },
        }, "Сохранить заметку"),
        saved,
      ),
    );
  }

  async restart() {
    if (!confirm("Начать заново? Текущая попытка будет отмечена как брошенная, черновик удалён.")) return;
    await api(`/api/tasks/${encodeURIComponent(this.task.id)}/attempts`, {
      method: "POST",
      body: { restart: true },
    });
    location.reload();
  }

  dispose() {
    this.disposed = true;
    for (const id of this.timers) clearInterval(id);
    clearTimeout(this.saveTimer);
    this.timers = [];
    this.stream?.close();
    for (const ed of this.editors.values()) ed.destroy();
    this.editors.clear();
    document.removeEventListener("visibilitychange", this.onVisibility);
    // Best effort: keep the draft and the activity that were not flushed yet.
    this.flushActivity();
    this.save();
  }
}

export function renderDiff(d, leftLabel, rightLabel) {
  const head = d.identical
    ? `${d.path} — совпадает`
    : `${d.path} — +${d.added} / -${d.removed}   (${leftLabel} | ${rightLabel})`;

  if (d.identical) {
    return el("div", { class: "diff" }, el("div", { class: "diff-head" }, head));
  }

  const rows = d.rows.map((r) =>
    el("tr", { class: r.op },
      el("td", { class: "num" }, r.left_num || ""),
      el("td", { class: "side left" }, r.left),
      el("td", { class: "num" }, r.right_num || ""),
      el("td", { class: "side right" }, r.right),
    ),
  );

  return el("div", { class: "diff" },
    el("div", { class: "diff-head" }, head),
    el("table", { class: "diff-table" }, el("tbody", {}, rows)),
  );
}
