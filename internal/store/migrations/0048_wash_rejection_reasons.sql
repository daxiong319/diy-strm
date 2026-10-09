-- T31 · 洗版结构化驳回理由 + 规则指纹
--
-- ⚠️ 本迁移只**加列**，不改任何既有列的语义、不动已有数据。
--
-- 为什么是「加两列」而不是「把 trace 改成 JSON」（方案 B）：
--   trace 是**给人读**的一句话（「分辨率 1080<2160（新差）」），
--   驳回理由是**给机器查**的稳定枚举。两种消费方对同一件事的要求相反：
--   人读的那份要能随手改措辞、不能因为机器解析不了就不写；
--   机器读的那份 code 必须稳定、不能因为文案润色就变。
--   合成一个字段的必然结果是：为了机器稳定，文案不敢改了；
--   或者为了文案自由，统计口径随改版漂移（「分辨率不够」和
--   「达不到最低分辨率」是同一件事，统计出来算两条）。
--   所以是两份，界面上一起展示、只有查询走 reject_reasons。
--
-- ⚠️ 存量行的 reject_reasons 是空串而不是猜一个值：
--   历史记录没有结构化理由，填一个「no_comparable_dimension」之类的
--   猜测值会让统计凭空多出一批从没发生过的驳回。空串 = 「当时没记」，
--   统计时要能区分「空串」与「确实没有理由」。
--
-- ⚠️ 不加索引：这两个列都不在查询条件上。
--   「按 reason_code 统计」这类查询走的是 JSON 里的字符串，
--   要真做聚合应该另建投影表而不是给 TEXT 加索引 ——
--   SQLite 在 TEXT 上建了索引也不会用于 JSON 数组的成员查询，
--   建了只是白付写入代价。

ALTER TABLE media_upgrade_records ADD COLUMN reject_reasons TEXT NOT NULL DEFAULT '';
ALTER TABLE media_upgrade_records ADD COLUMN rule_fingerprint TEXT NOT NULL DEFAULT '';