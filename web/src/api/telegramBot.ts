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

export interface BotConfig {
  enabled: boolean;
  /** token 读不回明文，只告诉你配没配。 */
  token_configured: boolean;
  allowed_users: number[];
  allowed_chats: number[];
  group_link: boolean;
  commands: BotCommand[];
  webhook_path: string;
}

/** 保存结果。token 传空串 = 保持原值（前端读不到明文，不能让用户改白名单时把 token 抹掉）。 */
export interface BotConfigSave {
  enabled: boolean;
  token_configured: boolean;
}

export const fetchBotConfig = () => http.get<BotConfig>("/admin/telegram/bot");

export const saveBotConfig = (payload: {
  enabled?: boolean;
  token?: string;
  allowed_users?: string;
  allowed_chats?: string;
  group_link?: boolean;
}) => http.put<BotConfigSave>("/admin/telegram/bot", payload);

export const TIER_LABELS: Record<BotCommandTier, string> = {
  read: "只读",
  write: "会改动数据",
  super: "仅管理员",
};