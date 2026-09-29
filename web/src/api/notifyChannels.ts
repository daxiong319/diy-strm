import { http } from "./client";

export interface NotifyChannelFieldOption {
  value: string;
  label: string;
}

export interface NotifyChannelFieldMeta {
  key: string;
  label: string;
  type: string; // text/password/select/textarea
  placeholder?: string;
  hint?: string;
  rows?: number;
  options?: NotifyChannelFieldOption[];
}

export interface NotifyChannelMeta {
  id: string;
  title: string;
  required: string[];
  note?: string;
  fields: NotifyChannelFieldMeta[];
}

export interface NotifyChannelRecord {
  id?: number;
  type: string;
  name: string;
  config: Record<string, string>;
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface NotifyChannelInput {
  type: string;
  name?: string;
  config: Record<string, string>;
  enabled?: boolean;
}

export function fetchNotifyChannelMeta() {
  return http.get<{ items: NotifyChannelMeta[] }>("/admin/notify-channels/meta");
}

export function fetchNotifyChannels() {
  return http.get<{ items: NotifyChannelRecord[] }>("/admin/notify-channels");
}

export function createNotifyChannel(body: NotifyChannelInput) {
  return http.post<NotifyChannelRecord>("/admin/notify-channels", body);
}

export function updateNotifyChannel(id: number, body: Partial<NotifyChannelInput>) {
  return http.put<NotifyChannelRecord>(`/admin/notify-channels/${id}`, body);
}

export function deleteNotifyChannel(id: number) {
  return http.del<{ id: number }>(`/admin/notify-channels/${id}`);
}

export function testNotifyChannel(body: { type: string; config: Record<string, string>; title?: string; content?: string }) {
  return http.post<Record<string, never>>("/admin/notify-channels/test", body);
}
