-- 0002_llm_config
--
-- BYOK（Bring Your Own Key）的承载表。两张：
--   llm_providers   一个厂商接入（Base URL + 加密后的 Key）
--   llm_models      挂在某个 provider 下的一个模型（chat 或 embedding）
--
-- 这一步不 ALTER document_chunks / memories 的 embedding 列——
-- 那是探测出真实维度 N 之后才做的事（代码架构设计 §5.2 的 BootstrapEmbedding），
-- 迁移只管建表，不管探测。

-- ── LLM Provider ────────────────────────────────────────────────
-- base_url 和 api_key 成对：一个 provider 就是"一个 OpenAI 兼容端点 + 一个 Key"。
--
-- api_key_encrypted 是 bytea，不是 text：
--   内容是 platform.SecretBox.Seal 的输出（AES-GCM 密文 + nonce 的拼接），
--   是二进制数据，不是可打印字符串。存成 text 需要额外编码（base64），
--   多一层转换、还多占空间；bytea 直接存原始字节。
CREATE TABLE llm_providers (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    base_url          text        NOT NULL,
    api_key_encrypted bytea       NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- ── LLM Model ───────────────────────────────────────────────────
-- 一个 provider 下可以挂多个模型（比如一个 chat、一个 embedding），
-- 所以外键指向 llm_providers，删 provider 时级联删它名下的模型。
--
-- model_id / context_window / max_output_tokens / tokenizer_type 全部手填：
--   OpenAI 兼容 API 不保证能自动查询这些信息（代码架构设计 §5 的能力边界）。
--
-- capabilities 是 text[] 不是 jsonb：只是一组标签（chat/streaming/tool_calling/
-- embedding/reasoning），不需要嵌套结构，text[] 能直接用 = ANY() 查询。
--
-- embedding_dim 只对 kind = 'embedding' 的行有意义，默认 0 表示"还没探测"。
-- 【这是维度的唯一存放处】document_chunks / memories 的 embedding 列本身会被
-- ALTER 成 halfvec(N)，维度已经在列类型里了，行内再存一次是冗余——
-- 这里的 embedding_dim 记的是"探测结果"这个事实，不是给检索用的。
CREATE TABLE llm_models (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id       uuid        NOT NULL
                      REFERENCES llm_providers(id) ON DELETE CASCADE,
    model_id          text        NOT NULL,
    kind              text        NOT NULL, -- chat | embedding
    capabilities      text[]      NOT NULL DEFAULT '{}',
    context_window    int         NOT NULL DEFAULT 0,
    max_output_tokens int         NOT NULL DEFAULT 0,
    tokenizer_type    text        NOT NULL DEFAULT '',
    embedding_dim     int         NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),

    -- kind 只有两个合法值，数据库层面挡住第三种——
    -- 应用层校验漏了的话，这一条兜底。
    CONSTRAINT llm_models_kind_check CHECK (kind IN ('chat', 'embedding'))
);

CREATE INDEX llm_models_provider_id_idx ON llm_models (provider_id);
