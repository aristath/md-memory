import MarkdownIt from "markdown-it";

const markdown = new MarkdownIt({ html: false, linkify: true, breaks: true });
const local = {
  get(key, fallback = "") {
    try {
      return localStorage.getItem(key) ?? fallback;
    } catch {
      return fallback;
    }
  },
  set(key, value) {
    try {
      if (value === null) localStorage.removeItem(key);
      else localStorage.setItem(key, value);
    } catch {
      /* Accepted messages are stored by the server. */
    }
  },
};
const newID = () =>
  Array.from(crypto.getRandomValues(new Uint8Array(16)), (n) =>
    n.toString(16).padStart(2, "0"),
  ).join("");

export function chatUI(request) {
  let events,
    watch = 0,
    settingsGeneration = 0;
  return {
    tab: "chat",
    chatUser: local.get("songya-chat-user"),
    chatSessions: [],
    chatSessionID: "",
    chatState: null,
    chatInput: "",
    chatSending: false,
    chatPending: null,
    chatError: "",
    chatConnection: "",
    chatFollow: true,
    chatAddingUser: false,
    chatNewUser: "",
    chatSettings: {
      base_url: "",
      model: "",
      temperature: 0.15,
      configured: false,
      has_api_key: false,
    },
    chatSettingsDraft: {
      base_url: "",
      model: "",
      temperature: 0.15,
      api_key: "",
      clear_key: false,
    },
    chatSettingsBusy: false,
    chatSettingsError: "",
    chatSettingsNotice: "",
    chatModels: [],
    async initChat() {
      const generation = settingsGeneration;
      await Promise.allSettled([
        request("/api/chat/settings")
          .then((value) => {
            if (generation === settingsGeneration) this.chatSettings = value;
          })
          .catch((e) => {
            this.chatError = e.message;
          }),
        this.refreshChatSessions(),
      ]);
      if (!this.chatUser) this.chatUser = this.chatUsers()[0] || "";
      try {
        this.chatPending = JSON.parse(local.get("songya-chat-pending", "null"));
      } catch {
        this.chatPending = null;
      }
      if (this.chatPending) {
        this.chatUser = this.chatPending.user;
        this.chatSessionID = this.chatPending.id;
        this.chatInput = local.get("songya-chat-draft:" + this.chatUser);
        await this.selectChat();
        await this.flushChatPending();
      } else await this.changeChatUser();
    },
    chatUsers() {
      return [
        ...new Set([
          ...Object.keys(this.owners),
          ...this.chatSessions.map((c) => c.user),
          ...(this.chatUser ? [this.chatUser] : []),
        ]),
      ].sort();
    },
    chatHistory() {
      return this.chatSessions.filter((c) => c.user === this.chatUser);
    },
    async refreshChatSessions() {
      try {
        const value = await request("/api/chat/sessions");
        this.chatSessions = value.sessions;
        if (value.diagnostics?.length)
          this.chatError = value.diagnostics.join("\n");
      } catch (e) {
        this.chatError = e.message;
      }
    },
    async changeChatUser() {
      local.set("songya-chat-user", this.chatUser);
      this.chatInput = local.get("songya-chat-draft:" + this.chatUser);
      this.chatSessionID = this.chatHistory()[0]?.id || "";
      await this.selectChat();
    },
    addChatUser() {
      if (this.chatSending || this.chatPending) return;
      const user = this.chatNewUser.trim();
      if (!user || user === "all") {
        this.chatError =
          "Enter one person's user name; ‘all’ selects multiple collections.";
        return;
      }
      this.chatUser = user;
      this.chatNewUser = "";
      this.chatAddingUser = false;
      this.changeChatUser();
    },
    async selectChat() {
      events?.close();
      watch++;
      this.chatState = null;
      this.chatError = "";
      this.chatConnection = "";
      this.chatFollow = true;
      if (!this.chatSessionID) return;
      const ticket = watch;
      try {
        const value = await request("/api/chat/sessions/" + this.chatSessionID);
        if (ticket !== watch) return;
        this.applyChat(value);
        if (["pending", "running"].includes(value.status)) this.watchChat();
      } catch (e) {
        if (ticket === watch) this.chatError = e.message;
      }
    },
    chatBusy() {
      return ["pending", "running"].includes(this.chatState?.status);
    },
    chatMessages() {
      const messages = this.chatState?.messages || [];
      const visible = messages.filter(
        (m) => m.role === "user" || m.role === "assistant",
      );
      const queue = this.chatState?.queue || [];
      const known = new Set([
        ...messages.map((m) => m.turn_id),
        ...queue.map((m) => m.id),
      ]);
      return [
        ...visible,
        ...(this.chatState?.draft ? [this.chatState.draft] : []),
        ...queue.map((m) => ({
          role: "user",
          content: m.text,
          status: "Queued",
        })),
        ...(this.chatPending
          ? [this.chatPending, ...(this.chatPending.next || [])].flatMap(
              (m, i) =>
                known.has(m.request_id)
                  ? []
                  : [
                      {
                        role: "user",
                        content: m.text,
                        status: i
                          ? "Waiting to send"
                          : this.chatSending
                            ? "Sending…"
                            : "Not sent · retry",
                      },
                    ],
            )
          : []),
      ];
    },
    chatToolResult(id) {
      return (
        [...(this.chatState?.messages || [])]
          .reverse()
          .find((m) => m.role === "tool" && m.tool_call_id === id)?.content ||
        "Waiting for the tool result…"
      );
    },
    chatMarkdown(text) {
      return markdown.render(text || "");
    },
    applyChat(value) {
      if (
        this.chatState?.id === value.id &&
        this.chatState.revision > value.revision
      )
        return;
      const wasBusy = this.chatBusy();
      const follow = this.chatFollow;
      this.chatState = value;
      this.chatSessionID = value.id;
      this.chatConnection = "";
      if (!this.chatSessions.some((c) => c.id === value.id))
        this.chatSessions.unshift(value);
      this.$nextTick(() => {
        if (follow) {
          this.scrollChat();
          this.chatFollow = true;
          // Layout can shrink the log when progress controls become visible.
          requestAnimationFrame(() => {
            if (this.chatFollow) this.scrollChat();
          });
        }
      });
      if (!this.chatBusy()) {
        events?.close();
        this.refreshChatSessions();
        if (wasBusy) {
          this.refreshGraph();
          this.refresh();
        }
      }
    },
    scrollChat() {
      const log = this.$refs.chatLog;
      if (log) log.scrollTop = log.scrollHeight;
    },
    followChat() {
      this.chatFollow = true;
      this.scrollChat();
    },
    keepChatDraft() {
      local.set("songya-chat-draft:" + this.chatUser, this.chatInput);
    },
    watchChat() {
      events?.close();
      const ticket = ++watch;
      events = new EventSource(
        "/api/chat/sessions/" + this.chatSessionID + "/events",
      );
      events.onmessage = (event) => {
        if (ticket === watch) this.applyChat(JSON.parse(event.data));
      };
      events.onerror = () => {
        if (ticket === watch && this.chatBusy())
          this.chatConnection = "Reconnecting… Your accepted message is saved.";
      };
      events.addEventListener("unavailable", (event) => {
        if (ticket !== watch) return;
        this.chatError = JSON.parse(event.data).error;
        this.chatConnection = "Conversation temporarily unavailable.";
        events.close();
      });
    },
    async sendChat() {
      if (!this.chatUser || !this.chatSettings.configured) return;
      if (this.chatInput.trim()) {
        const message = {
          id: this.chatSessionID || this.chatPending?.id || newID(),
          request_id: newID(),
          user: this.chatUser,
          text: this.chatInput,
        };
        this.chatSessionID = message.id;
        this.chatPending = this.chatPending
          ? {
              ...this.chatPending,
              next: [...(this.chatPending.next || []), message],
            }
          : message;
        local.set("songya-chat-pending", JSON.stringify(this.chatPending));
        this.chatInput = "";
        this.keepChatDraft();
        this.chatFollow = true;
        this.$refs.chatInput?.focus();
        this.$nextTick(() => this.scrollChat());
      }
      await this.flushChatPending();
    },
    async flushChatPending() {
      if (this.chatSending || !this.chatPending) return;
      this.chatSending = true;
      this.chatError = "";
      try {
        while (this.chatPending) {
          const { next, ...message } = this.chatPending;
          const value = await request("/api/chat", {
            method: "POST",
            body: JSON.stringify(message),
          });
          const [head, ...tail] = this.chatPending.next || [];
          this.chatPending = head ? { ...head, next: tail } : null;
          local.set(
            "songya-chat-pending",
            this.chatPending ? JSON.stringify(this.chatPending) : null,
          );
          this.applyChat(value);
          if (this.chatBusy()) this.watchChat();
        }
      } catch (e) {
        this.chatError = e.message + " Retry sends the same request.";
      } finally {
        this.chatSending = false;
      }
    },
    async retryChat() {
      this.chatError = "";
      const ticket = watch;
      const id = this.chatSessionID;
      try {
        const value = await request("/api/chat/sessions/" + id + "/retry", {
          method: "POST",
        });
        if (ticket !== watch) return;
        this.chatFollow = true;
        this.applyChat(value);
        this.watchChat();
      } catch (e) {
        if (ticket === watch) this.chatError = e.message;
      }
    },
    async pauseChat() {
      const ticket = watch;
      const id = this.chatSessionID;
      try {
        const value = await request("/api/chat/sessions/" + id + "/pause", {
          method: "POST",
        });
        if (ticket === watch) this.applyChat(value);
      } catch (e) {
        if (ticket === watch) this.chatError = e.message;
      }
    },
    openChatSettings() {
      this.chatSettingsDraft = {
        base_url: this.chatSettings.base_url,
        model: this.chatSettings.model,
        temperature: this.chatSettings.temperature ?? 0.15,
        api_key: "",
        clear_key: false,
      };
      this.chatSettingsError = "";
      this.chatSettingsNotice = "";
      this.$refs.chatSettingsDialog.showModal();
    },
    closeChatSettings() {
      this.$refs.chatSettingsDialog.close();
    },
    async saveChatSettings(close = true) {
      settingsGeneration++;
      this.chatSettingsBusy = true;
      this.chatSettingsError = "";
      this.chatSettingsNotice = "";
      try {
        const draft = this.chatSettingsDraft;
        const payload = {
          base_url: draft.base_url,
          model: draft.model,
          temperature: draft.temperature,
        };
        if (draft.api_key || draft.clear_key)
          payload.api_key = draft.clear_key ? "" : draft.api_key;
        this.chatSettings = await request("/api/chat/settings", {
          method: "PUT",
          body: JSON.stringify(payload),
        });
        this.chatSettingsDraft.api_key = "";
        this.chatSettingsDraft.clear_key = false;
        if (close) this.closeChatSettings();
        return true;
      } catch (e) {
        this.chatSettingsError = e.message;
        return false;
      } finally {
        this.chatSettingsBusy = false;
      }
    },
    async loadChatModels() {
      if (!(await this.saveChatSettings(false))) return;
      this.chatSettingsBusy = true;
      try {
        const value = await request("/api/chat/models");
        this.chatModels = value.models;
        this.chatSettingsNotice = `Connected. ${value.models.length} models available.`;
      } catch (e) {
        this.chatSettingsError = e.message;
      } finally {
        this.chatSettingsBusy = false;
      }
    },
  };
}
