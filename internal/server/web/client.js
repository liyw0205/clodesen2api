(() => {
  "use strict";
  window.createAdminClient = ({ onUnauthorized, timeoutMS = 60000 }) => {
    const pending = new Set();
    async function request(path, options = {}) {
      const { timeout = timeoutMS, signal, responseType = "json", ...init } = options;
      const controller = new AbortController();
      const abort = () => controller.abort(signal.reason);
      if (signal?.aborted) abort();
      else signal?.addEventListener("abort", abort, { once: true });
      const timer = window.setTimeout(() => controller.abort(new DOMException("请求超时，请重试", "TimeoutError")), timeout);
      pending.add(controller);
      try {
        const headers = new Headers(init.headers || {});
        if (typeof init.body === "string" && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
        const response = await fetch(path, { credentials: "same-origin", cache: "no-store", ...init, headers, signal: controller.signal });
        const payload = response.headers.get("content-type")?.includes("application/json") ? await response.json() : responseType === "text" ? await response.text() : null;
        controller.signal.throwIfAborted();
        if (response.status === 401 && path !== "/admin/api/session") {
          onUnauthorized();
          throw new Error("登录状态已过期，请重新登录");
        }
        if (!response.ok) {
          const error = new Error(typeof payload?.error === "string" ? payload.error : payload?.error?.message || "请求失败");
          error.status = response.status;
          error.retryAfter = response.headers.get("Retry-After");
          throw error;
        }
        return payload;
      } catch (error) {
        if (controller.signal.aborted) throw controller.signal.reason;
        throw error;
      } finally {
        window.clearTimeout(timer);
        signal?.removeEventListener("abort", abort);
        pending.delete(controller);
      }
    }
    return { request, clear() { for (const controller of pending) controller.abort(); pending.clear(); } };
  };
})();
