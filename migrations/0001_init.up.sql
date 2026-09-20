-- 0001_init
--
-- ConGoRAG 的初始表结构。四张表：
--   knowledge_bases   知识库
--   documents         文档（属于某个知识库）
--   document_chunks   文档分块（属于某个文档，向量存在这里）
--   memories          长期记忆（独立，服务对话而不是知识库）

-- ── 扩展 ────────────────────────────────────────────────────────
-- halfvec 这个类型由 pgvector 提供，所以这一句必须在所有建表语句之前。
-- 不装的话，后面用到 halfvec 的地方会报 type "halfvec" does not exist。
-- IF NOT EXISTS 是为了幂等——万一库里已经装了，不报错。
-- 装扩展需要超级用户权限；本项目默认用 postgres 连，所以没问题。
CREATE EXTENSION IF NOT EXISTS vector;

-- ── 知识库 ──────────────────────────────────────────────────────
-- 前端左栏"知识库列表"的每一行，就是这里的一条记录。
CREATE TABLE knowledge_bases (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- ── 文档 ────────────────────────────────────────────────────────
-- filename    用户上传时的原始文件名，给人看的
-- storage_key 文件在磁盘上的实际名字，调用方生成的 uuid + 扩展名
--             两者必须分开：用户可能上传两个都叫"笔记.md"的文件，
--             磁盘上要是也用原名，第二个会覆盖第一个
CREATE TABLE documents (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    knowledge_base_id uuid        NOT NULL
                      REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    filename          text        NOT NULL,
    storage_key       text        NOT NULL UNIQUE,
    status            text        NOT NULL DEFAULT 'queued',
    byte_size         bigint      NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- 外键列上建索引。删知识库时数据库要按这一列找出它下面的所有文档，
-- 没索引就得全表扫描。
CREATE INDEX documents_knowledge_base_id_idx ON documents (knowledge_base_id);

-- ── 文档分块 ────────────────────────────────────────────────────
-- 这是向量真正存放的地方。RAG 检索的单位是"一小段"，不是整篇文档。
--
-- embedding 是 halfvec，故意不写维度：
--   维度由用户配置的 embedding 模型决定，代码里猜不出来。
--   流程是——迁移先建一个不带维度的列，引导页探测出维度 N 之后，
--   用 ALTER 把列改成 halfvec(N)，那时才能建向量索引。
--   不带维度的 halfvec 能存任意维度，但建不了索引。
--
-- embedding 没有 NOT NULL：文档刚入库、还没向量化时这一列是空的。
--
-- embedding_model 记录这个向量是哪个模型算出来的。检索时必须带上它做过滤：
--   将来用户换模型，表里会同时存在不同维度的向量，
--   不加过滤直接算距离会报错（不是变慢，是直接报错）。
CREATE TABLE document_chunks (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id     uuid        NOT NULL
                    REFERENCES documents(id) ON DELETE CASCADE,
    content         text        NOT NULL,
    token_count     int         NOT NULL DEFAULT 0,
    embedding       halfvec,
    embedding_model text,
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX document_chunks_document_id_idx ON document_chunks (document_id);

-- ── 长期记忆 ────────────────────────────────────────────────────
-- 结构上和 document_chunks 几乎一样，因为要做的事是同一件：
-- 存一段文字 + 它的向量 + 这个向量是哪个模型算的。
--
-- 但它们存的语义不同：
--   document_chunks  用户上传的文档切出来的片段
--   memories         系统从对话里抽出来的用户偏好 / 事实
-- 使用场景、生命周期、检索范围都不一样，所以不合并。
CREATE TABLE memories (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope           text        NOT NULL,
    content         text        NOT NULL,
    embedding       halfvec,
    embedding_model text,
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);
