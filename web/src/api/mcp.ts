import { http } from "./client";

// MCP 服务与内置智能助理。
//
// 站内端点（配置 / 工具清单 / 对话）挂在 /admin/mcp 下、由 requireAdmin 保护，
// 对应后端 internal/api/mcp.go 的 RegisterMcpAdminRoutes。
// 对外的 MCP 协议端点固定为 /api/mcp，供外部客户端（Claude Desktop / Cursor 等）
// 用 API Key 调用，本文件只负责把它显示给用户，不做调用。

export interface McpConfig {
  enabled: boolean;
  /** 是否允许 LLM 调用写操作工具（网盘重命名/移动/新建/删除等）。 */
  allow_write_tools: boolean;
  /** 被禁用的工具名清单。 */
  disabled_tools: string[];
  max_tool_rounds: number;
  timeout: number;

  /** 是否允许站内智能助理对话。 */
  assistant_enabled: boolean;
  /** 助理自己的模型配置；留空则回落到「AI 识别设置」。 */
  assistant_base_url: string;
  assistant_model_name: string;
  assistant_prompt: string;
  /**
   * 助理 API Key 是否已配置。
   * 后端只回传这个布尔值，**从不回传密钥明文**。
   */
  assistant_api_key_set: boolean;

  /** 实际生效的配置（含向「AI 识别设置」回落后的结果），密钥同样只以 _set 形式回传。 */
  effective_base_url: string;
  effective_model_name: string;
  effective_prompt: string;
  effective_api_key_set: boolean;
}

export interface McpConfigPatch {
  enabled?: boolean;
  allow_write_tools?: boolean;
  disabled_tools?: string[];
  max_tool_rounds?: number;
  timeout?: number;

  assistant_enabled?: boolean;
  assistant_base_url?: string;
  assistant_model_name?: string;
  assistant_prompt?: string;
  /** 传空串表示清除已保存的密钥。 */
  assistant_api_key?: string;
}

export interface McpTool {
  name: string;
  description: string;
  /** 只读工具可直接调用；写操作工具需要用户明确同意。 */
  read_only: boolean;
  /** 已在配置中启用（未被 disabled_tools 排除）。 */
  enabled: boolean;
  /** 依赖的能力是否已就绪，未就绪时调用会失败。 */
  available: boolean;
  /** 是否对模型可见。 */
  visible: boolean;
}

export interface McpConfigEnvelope {
  config: McpConfig;
  tools: McpTool[];
  server_url: string;
}

export interface McpChatMessage {
  role: "user" | "assistant" | string;
  content: string;
  tool_calls?: McpToolCall[];
}

export interface McpToolCall {
  name: string;
  arguments?: string;
  result?: string;
  error?: string;
}

export interface McpSession {
  session_id: string;
  title: string;
  created_at: string;
  updated_at: string;
  count: number;
}

/** 助理流式对话的事件类型，与后端 SSE 事件名逐字对齐。 */
export type McpAssistantEvent =
  | { type: "init"; session_id: string }
  | { type: "delta"; content: string }
  | { type: "tool_call"; tool_name: string; tool_args: string }
  | { type: "tool_result"; tool_name: string; tool_result?: string; tool_error?: string }
  | { type: "error"; message: string }
  | { type: "done"; reply?: string; rounds?: number };

// 注意：MCP 的管理端点挂在 /api/mcp 下，没有 /admin 段
// （internal/api/router.go 里 RegisterMcpAdminRoutes 位于 requireAdmin 组，
//  但该组挂在 /api 而非 /api/admin）。因此这里的 base 不能写成 "/admin/mcp"，
//  否则所有请求都会打到 /api/admin/mcp/* 而得到 404。
const base = "/mcp";

export const mcpApi = {
  getConfig: () => http.get<McpConfigEnvelope>(`${base}/config`),
  updateConfig: (patch: McpConfigPatch) => http.put<McpConfigEnvelope>(`${base}/config`, patch),
  tools: () => http.get<{ tools: McpTool[] }>(`${base}/tools`),

  testAssistant: () => http.post<{ message: string }>(`${base}/assistant/test`, {}),

  sessions: () => http.get<{ sessions: McpSession[] }>(`${base}/assistant/sessions`),
  sessionMessages: (sessionID: string) =>
    http.get<{ messages: McpChatMessage[] }>(
      `${base}/assistant/sessions/${encodeURIComponent(sessionID)}`,
    ),
  deleteSession: (sessionID: string) =>
    http.del<{ deleted: boolean }>(`${base}/assistant/sessions/${encodeURIComponent(sessionID)}`),
};

/**
 * 以 SSE 流式方式与智能助理对话。
 *
 * 这里不用 EventSource：EventSource 只能发 GET、无法携带请求体，而对话需要 POST
 * 一段 JSON（消息 + 会话 ID + 历史）。因此改用 fetch + ReadableStream 手工解析
 * SSE 帧，与 web/src/api/files.ts 的 reader 循环保持同一风格。
 *
 * 返回一个 abort 函数，组件卸载时调用即可中断请求。
 */
export function streamMcpAssistant(
  payload: { message: string; session_id?: string; history?: McpChatMessage[] },
  onEvent: (event: McpAssistantEvent) => void,
  onError?: (message: string) => void,
): () => void {
  const controller = new AbortController();

  void (async () => {
    let response: Response;
    try {
      response = await fetch(`/api${base}/assistant/stream`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
        signal: controller.signal,
      });
    } catch (error) {
      if (!controller.signal.aborted) {
        onError?.(error instanceof Error ? error.message : "无法连接到智能助理");
      }
      return;
    }

    if (!response.ok) {
      // 后端出错时可能返回 JSON 错误体也可能是纯文本，两种都兜住。
      let message = `请求失败 (${response.status})`;
      try {
        const body = (await response.json()) as { message?: string };
        if (body.message) message = body.message;
      } catch {
        // 保留状态码信息。
      }
      onError?.(message);
      return;
    }

    const reader = response.body?.getReader();
    if (!reader) {
      onError?.("当前浏览器不支持流式响应");
      return;
    }

    const decoder = new TextDecoder();
    let buffer = "";

    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        // SSE 以空行分隔帧；保留最后一段（可能被截断）留到下一轮拼接。
        let boundary = buffer.indexOf("\n\n");
        while (boundary !== -1) {
          const frame = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          const event = parseSseFrame(frame);
          if (event) onEvent(event);
          boundary = buffer.indexOf("\n\n");
        }
      }
      // 服务端未以空行收尾时，处理残留的最后一帧。
      const tail = parseSseFrame(buffer);
      if (tail) onEvent(tail);
    } catch (error) {
      if (!controller.signal.aborted) {
        onError?.(error instanceof Error ? error.message : "流式响应中断");
      }
      return;
    } finally {
      reader.releaseLock();
    }
  })();

  return () => controller.abort();
}

/** 解析单个 SSE 帧（形如 `event: delta\ndata: {...}`），无法识别时返回 null。 */
function parseSseFrame(frame: string): McpAssistantEvent | null {
  let name = "";
  const dataLines: string[] = [];

  for (const rawLine of frame.split("\n")) {
    const line = rawLine.replace(/\r$/, "");
    if (line.startsWith("event:")) {
      name = line.slice("event:".length).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice("data:".length).replace(/^ /, ""));
    }
  }

  if (!name) return null;
  const data = dataLines.join("\n");

  let parsed: Record<string, unknown> = {};
  if (data) {
    try {
      parsed = JSON.parse(data) as Record<string, unknown>;
    } catch {
      // 后端对 data 做了 JSON 序列化；解析失败时按纯文本内容处理，
      // 以免单个坏帧让整轮对话静默中断。
      return name === "delta" ? { type: "delta", content: data } : null;
    }
  }

  switch (name) {
    case "init":
      return { type: "init", session_id: asString(parsed.session_id) };
    case "delta":
      return { type: "delta", content: asString(parsed.content) };
    case "tool_call":
      return {
        type: "tool_call",
        tool_name: asString(parsed.tool_name),
        tool_args: asString(parsed.tool_args),
      };
    case "tool_result":
      return {
        type: "tool_result",
        tool_name: asString(parsed.tool_name),
        tool_result: optionalString(parsed.tool_result),
        tool_error: optionalString(parsed.tool_error),
      };
    case "error":
      return { type: "error", message: asString(parsed.message) };
    case "done":
      return {
        type: "done",
        reply: optionalString(parsed.reply),
        rounds: typeof parsed.rounds === "number" ? parsed.rounds : undefined,
      };
    default:
      return null;
  }
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}
