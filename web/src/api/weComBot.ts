import { http } from "@/api/client";

/** 一条命令的权限档位：只读 / 会改动数据 / 仅管理员。 */
export type BotCommandTier = "read" | "write" | "super";

export interface BotCommand {
  name: string;
  aliases: string[];
  summary: string;
  usage: string;
  tier: BotCommandTier;
  /** false = 本版只回「暂未支持」，不是漏实现。 */
  supported: boolean;
}

/** 企业微信智能机器人的配置。 */
export interface WeComBotConfig {
  enabled: boolean;
  /** Secret 与 EncodingAESKey 都读不回明文，只告诉你配没配。 */
  token_configured: boolean;
  aes_configured: boolean;
  allowed_users: number[];
  allowed_chats: number[];
  group_link: boolean;
  corp_id: string;
  agent_id: string;
  commands: BotCommand[];
  /** 要填进企微后台「接收消息」的 URL 路径。 */
  callback_path: string;
}

/** 保存结果。secret 传空串 = 保持原值。 */
export interface WeComBotConfigSave {
  enabled: boolean;
  token_configured: boolean;
  aes_configured: boolean;
  ready: boolean;
}

export const fetchWeComBotConfig = () => http.get<WeComBotConfig>("/admin/telegram/wecom-bot");

export const saveWeComBotConfig = (payload: {
  enabled?: boolean;
  token?: string;
  aes_key?: string;
  allowed_users?: string;
  allowed_chats?: string;
  group_link?: boolean;
  corp_id?: string;
  agent_id?: string;
}) => http.put<WeComBotConfigSave>("/admin/telegram/wecom-bot", payload);

export const TIER_LABELS: Record<BotCommandTier, string> = {
  read: "只读",
  write: "会改动数据",
  super: "仅管理员",
};
