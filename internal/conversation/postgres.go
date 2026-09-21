// Repo 的 PostgreSQL 实现。所有 SQL 都收敛在这个文件里。
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Repo = (*PgRepo)(nil)

// PgRepo 是无状态的，连接由调用方通过 q 参数传入——和 knowledge.PgRepo
// 同一个模式。
type PgRepo struct{}

func NewPgRepo() *PgRepo {
	return &PgRepo{}
}

func (r *PgRepo) CreateConversation(ctx context.Context, q platform.Querier, c *Conversation) error {
	_, err := q.Exec(ctx,
		`INSERT INTO conversations (id, title, knowledge_base_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		c.ID, c.Title, c.KnowledgeBaseID, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert conversation %s: %w", c.ID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) GetConversation(ctx context.Context, q platform.Querier, id uuid.UUID) (*Conversation, error) {
	c := &Conversation{}
	err := q.QueryRow(ctx,
		`SELECT id, title, knowledge_base_id, created_at, updated_at
		 FROM conversations WHERE id = $1`, id,
	).Scan(&c.ID, &c.Title, &c.KnowledgeBaseID, &c.CreatedAt, &c.UpdatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("conversation %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation %s: %w", id, platform.WrapPgErr(err))
	}
	return c, nil
}

func (r *PgRepo) AppendMessage(ctx context.Context, q platform.Querier, m *Message) error {
	_, err := q.Exec(ctx,
		`INSERT INTO messages (id, conversation_id, role, content, status, sequence_no, token_usage, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		m.ID, m.ConversationID, string(m.Role), m.Content, string(m.Status), m.SequenceNo,
		tokenUsageJSON(m.TokenUsage), m.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("append message %s: %w", m.ID, platform.WrapPgErr(err))
	}
	return nil
}

// tokenUsageJSON 把可能为 nil 的 *domain.TokenUsage 序列化成 jsonb 参数。
// 用户消息没有 token 用量,这里必须能安全地传 nil 进去存成 SQL NULL。
func tokenUsageJSON(tu *domain.TokenUsage) []byte {
	if tu == nil {
		return nil
	}
	b, err := json.Marshal(tu)
	if err != nil {
		// TokenUsage 只有两个 int 字段，序列化不会失败；
		// 真失败了返回 nil 比 panic 更安全——落库时这一列会是 NULL，
		// 不影响消息内容本身的正确性。
		return nil
	}
	return b
}

// NextSequenceNo 查当前最大序号后 +1。
//
// 【为什么不用独立的计数器表】调用方（Usecase.lockAndWriteInitialMessages）
// 已经在 advisory lock 保护的临界区内调用这个方法——直接查
// max(sequence_no) 在锁的保护下是安全的，不需要像 event_id 那样引入
// 一张独立的计数器表（event_id 的分配不在这把锁保护范围内）。
func (r *PgRepo) NextSequenceNo(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	var max *int64
	err := q.QueryRow(ctx,
		`SELECT MAX(sequence_no) FROM messages WHERE conversation_id = $1`, convID,
	).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("query max sequence_no of conversation %s: %w", convID, platform.WrapPgErr(err))
	}
	if max == nil {
		return 1, nil
	}
	return *max + 1, nil
}

func (r *PgRepo) RecentMessages(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo, beforeSequenceNo int64, limit int) ([]*Message, error) {
	// 【为什么是子查询 + 外层 ASC】要同时满足两件事："最近 limit 条"
	// 和"按旧 → 新返回"。直接 ORDER BY sequence_no ASC LIMIT n 拿到的是
	// 最旧的 n 条，方向对了内容错了；直接 DESC LIMIT n 内容对了方向错。
	// 内层按 DESC 取最近 n 条，外层重排成 ASC。
	//
	// beforeSequenceNo = 0 表示无上界——sequence_no 从 1 开始分配
	//（NextSequenceNo 空会话返回 1），0 不可能是一个真实存在的序号。
	//
	// 【为什么只挡上界、不加 status <> 'streaming' 谓词】上一轮中断留下的
	// streaming 行是助手已经生成的真实内容（客户端断开时不会补写完成态），
	// 按状态过滤会把它从历史里删掉，而 GET /messages 仍然把它展示给用户——
	// 模型看到的历史会和用户看到的转写不一致。本轮自己的占位行是空内容，
	// 由序号上界挡住；万一还有别的空行漏下来，itemsToLLMMessages 还会再
	// 过滤一次。
	rows, err := q.Query(ctx,
		`SELECT id, conversation_id, role, content, status, sequence_no, token_usage, created_at
		 FROM (
		     SELECT id, conversation_id, role, content, status, sequence_no, token_usage, created_at
		     FROM messages
		     WHERE conversation_id = $1 AND sequence_no > $2 AND ($3 = 0 OR sequence_no < $3)
		     ORDER BY sequence_no DESC
		     LIMIT $4
		 ) AS recent_messages
		 ORDER BY sequence_no ASC`,
		convID, afterSequenceNo, beforeSequenceNo, limit)
	if err != nil {
		return nil, fmt.Errorf("recent messages of conversation %s: %w", convID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *PgRepo) ListMessages(ctx context.Context, q platform.Querier, convID uuid.UUID) ([]*Message, error) {
	rows, err := q.Query(ctx,
		`SELECT id, conversation_id, role, content, status, sequence_no, token_usage, created_at
		 FROM messages
		 WHERE conversation_id = $1
		 ORDER BY sequence_no ASC`,
		convID)
	if err != nil {
		return nil, fmt.Errorf("list messages of conversation %s: %w", convID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *PgRepo) MessagesAfter(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo int64, limit int) ([]*Message, error) {
	rows, err := q.Query(ctx,
		`SELECT id, conversation_id, role, content, status, sequence_no, token_usage, created_at
		 FROM messages
		 WHERE conversation_id = $1 AND sequence_no > $2
		 ORDER BY sequence_no ASC
		 LIMIT $3`,
		convID, afterSequenceNo, limit)
	if err != nil {
		return nil, fmt.Errorf("messages after %d of conversation %s: %w", afterSequenceNo, convID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// LatestSequenceNo 只数已定稿（status = 'completed'）的行。
//
// 【为什么排除生成中的占位行】每轮 send 先把助手消息以空内容、
// status='streaming' 落库占位（usecase.go），这一行此刻的正文还没有定稿。
// 如果它算进水位，摘要任务就会在一个空占位行上被触发、还可能把
// covered_until_sequence_no 推过它——生成结束后写进去的正文既不在摘要里，
// 又被 RecentMessages（sequence_no > covered_until）排除，这条回答对模型
// 永久不可见。门控的意义是"攒够了多少条真正可以压缩的消息"，不是
// "序号涨到了多少"。
func (r *PgRepo) LatestSequenceNo(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	var max *int64
	err := q.QueryRow(ctx,
		`SELECT MAX(sequence_no) FROM messages WHERE conversation_id = $1 AND status = 'completed'`, convID,
	).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("latest sequence_no of conversation %s: %w", convID, platform.WrapPgErr(err))
	}
	if max == nil {
		return 0, nil
	}
	return *max, nil
}

func (r *PgRepo) ListConversationIDs(ctx context.Context, q platform.Querier) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM conversations`)
	if err != nil {
		return nil, fmt.Errorf("list conversation ids: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan conversation id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation ids: %w", err)
	}
	return out, nil
}

func scanMessages(rows pgx.Rows) ([]*Message, error) {
	var out []*Message
	for rows.Next() {
		m := &Message{}
		var role, status string
		var tokenUsageRaw []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &role, &m.Content, &status,
			&m.SequenceNo, &tokenUsageRaw, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.Role = domain.Role(role)
		m.Status = MessageStatus(status)
		if len(tokenUsageRaw) > 0 {
			var tu domain.TokenUsage
			if err := json.Unmarshal(tokenUsageRaw, &tu); err == nil {
				m.TokenUsage = &tu
			}
			// 反序列化失败不报错、不中断整个查询——那说明这一行的
			// token_usage 存了格式不对的历史数据，丢掉这一个字段
			// 比让整个消息列表加载失败更合理。
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return out, nil
}

func (r *PgRepo) UpdateMessageContent(ctx context.Context, q platform.Querier, id uuid.UUID, content string, status MessageStatus) error {
	tag, err := q.Exec(ctx,
		`UPDATE messages SET content = $2, status = $3 WHERE id = $1`,
		id, content, string(status))
	if err != nil {
		return fmt.Errorf("update message %s content: %w", id, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("message %s: %w", id, platform.ErrNotFound)
	}
	return nil
}

// NextEventID 实现 docs/sse-protocol.md「事件与续传」一节给出的 SQL。
func (r *PgRepo) NextEventID(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	var next int64
	err := q.QueryRow(ctx,
		`INSERT INTO conversation_counters (conversation_id, next_event_id)
		 VALUES ($1, 1)
		 ON CONFLICT (conversation_id) DO UPDATE
		   SET next_event_id = conversation_counters.next_event_id + 1
		 RETURNING next_event_id`,
		convID,
	).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("allocate next event id for conversation %s: %w", convID, platform.WrapPgErr(err))
	}
	return next, nil
}

func (r *PgRepo) AppendEvent(ctx context.Context, q platform.Querier, convID uuid.UUID, ev Event) error {
	_, err := q.Exec(ctx,
		`INSERT INTO conversation_events (conversation_id, event_id, payload) VALUES ($1, $2, $3)`,
		convID, ev.ID, ev.Payload,
	)
	if err != nil {
		return fmt.Errorf("append event %d of conversation %s: %w", ev.ID, convID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) EventsAfter(ctx context.Context, q platform.Querier, convID uuid.UUID, afterEventID int64) ([]Event, error) {
	rows, err := q.Query(ctx,
		`SELECT event_id, payload FROM conversation_events
		 WHERE conversation_id = $1 AND event_id > $2
		 ORDER BY event_id ASC`,
		convID, afterEventID)
	if err != nil {
		return nil, fmt.Errorf("events after %d of conversation %s: %w", afterEventID, convID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var ev Event
		var payload []byte
		if err := rows.Scan(&ev.ID, &payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		ev.Payload = payload
		// Type 从 payload 里的 "type" 字段还原——存表时没有单独一列存
		// event 类型（conversation_events 只有 event_id/payload 两列，
		// 见 0003 迁移），类型信息本身就编码在 payload 的 JSON 里。
		ev.Type = extractEventType(payload)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return out, nil
}

func (r *PgRepo) ReserveIdempotencyKey(ctx context.Context, q platform.Querier, rec *IdempotencyRecord, expiredBefore time.Time) error {
	// 【清理与插入必须同一次调用】两次并发预留如果各自「先清理、再插入」，
	// 中间会有一个两边都没覆盖到的窗口。调用方在 WithConversationLock 的
	// 事务里调这个方法，两者就天然原子。
	if _, err := q.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < $1`, expiredBefore); err != nil {
		return fmt.Errorf("prune expired idempotency keys: %w", platform.WrapPgErr(err))
	}

	_, err := q.Exec(ctx,
		`INSERT INTO idempotency_keys
		   (endpoint, idempotency_key, resource_type, resource_id,
		    first_event_id, request_fingerprint, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rec.Endpoint, rec.Key, rec.ResourceType, rec.ResourceID,
		rec.FirstEventID, rec.RequestFingerprint, rec.CreatedAt)
	if err != nil {
		// 撞主键 = 这个键已经被用掉了，是正常的重放场景，不是错误。
		// 分流全靠 WrapPgErr 按约束名判断（constraintIdempotencyKey），
		// 所以这里必须过它——漏了的话 23505 会以原始 pgconn.PgError 冒上去，
		// errors.Is(err, platform.ErrIdempotentHit) 为假，命中就永远走不到
		// 补发分支，客户端会收到一条 internal_error 的 error 帧、看不到答案。
		return fmt.Errorf("reserve idempotency key %s: %w", rec.Key, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) LookupIdempotencyKey(ctx context.Context, q platform.Querier, endpoint, key string) (*IdempotencyRecord, error) {
	rec := &IdempotencyRecord{}
	err := q.QueryRow(ctx,
		`SELECT endpoint, idempotency_key, resource_type, resource_id,
		        first_event_id, request_fingerprint, created_at
		 FROM idempotency_keys
		 WHERE endpoint = $1 AND idempotency_key = $2`,
		endpoint, key,
	).Scan(&rec.Endpoint, &rec.Key, &rec.ResourceType, &rec.ResourceID,
		&rec.FirstEventID, &rec.RequestFingerprint, &rec.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		// 预留之后又被 TTL 清理掉，或键压根没预留成功。两种情况都归成
		// 「查不到」，由调用方决定怎么收场。
		return nil, fmt.Errorf("idempotency key %s: %w", key, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup idempotency key %s: %w", key, platform.WrapPgErr(err))
	}
	return rec, nil
}

func (r *PgRepo) LastIssuedEventID(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	var last int64
	err := q.QueryRow(ctx,
		`SELECT next_event_id FROM conversation_counters WHERE conversation_id = $1`, convID,
	).Scan(&last)

	if errors.Is(err, pgx.ErrNoRows) {
		// 计数器行是 NextEventID 第一次被调用时才 INSERT 的，所以「查不到」
		// 等于「这个会话还一条事件都没发过」——0 是正确答案，不是错误。
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read last issued event id of conversation %s: %w", convID, platform.WrapPgErr(err))
	}
	return last, nil
}

func (r *PgRepo) GetSummary(ctx context.Context, q platform.Querier, convID uuid.UUID) (*Summary, error) {
	s := &Summary{}
	err := q.QueryRow(ctx,
		`SELECT conversation_id, summary, covered_until_sequence_no, updated_at
		 FROM conversation_summaries WHERE conversation_id = $1`, convID,
	).Scan(&s.ConversationID, &s.Summary, &s.CoveredUntilSequenceNo, &s.UpdatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("summary of conversation %s: %w", convID, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get summary of conversation %s: %w", convID, platform.WrapPgErr(err))
	}
	return s, nil
}

func (r *PgRepo) UpsertSummary(ctx context.Context, q platform.Querier, s *Summary) error {
	_, err := q.Exec(ctx,
		`INSERT INTO conversation_summaries (conversation_id, summary, covered_until_sequence_no, updated_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (conversation_id) DO UPDATE
		   SET summary = EXCLUDED.summary,
		       covered_until_sequence_no = EXCLUDED.covered_until_sequence_no,
		       updated_at = EXCLUDED.updated_at`,
		s.ConversationID, s.Summary, s.CoveredUntilSequenceNo, s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert summary of conversation %s: %w", s.ConversationID, platform.WrapPgErr(err))
	}
	return nil
}

// extractEventType 从 SSE 事件的 JSON payload 里取出 "type" 字段。
// Usecase 写入时把 {"type": "...", ...} 整个当 payload 存,这里只是
// 读回来时把类型摘出来单独放进 Event.Type,方便调用方按类型分支处理，
// 不用每次都自己解析一遍 JSON。
func extractEventType(payload []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return ""
	}
	return probe.Type
}

var _ LockedWriter = (*PgLockedWriter)(nil)

// PgLockedWriter 用 PostgreSQL 的会话级咨询锁（advisory lock）实现
// LockedWriter——它需要独占一条连接贯穿"加锁 → 干活 → 解锁"整个过程，
// 所以持有 *pgxpool.Pool 而不是通用的 platform.Querier：Acquire 出来的
// 是一条真实连接，事务/普通查询用的 Querier 接口表达不出"这条连接
// 要被单独占用一段时间"这件事。
type PgLockedWriter struct {
	pool poolAcquirer
}

// poolAcquirer 是 *pgxpool.Pool 用到的那一个方法的窄接口，
// 只是为了让这个文件不用在类型签名里写出具体的 pgxpool 类型名——
// 测试时可以塞一个假实现进来，不需要真的起一个连接池。
type poolAcquirer interface {
	Acquire(ctx context.Context) (*pgxpool.Conn, error)
}

func NewPgLockedWriter(pool *pgxpool.Pool) *PgLockedWriter {
	return &PgLockedWriter{pool: pool}
}

// lockKeySalt 是 advisory lock 键的固定盐值。
//
// PostgreSQL 的 pg_advisory_xact_lock 接单个 bigint 或者一对 int，
// 而会话 id 是 uuid——不能直接传。用 uuid 的高 64 位当 bigint 键，
// 加一个固定盐值是为了和"将来别的功能也用 advisory lock、但传的
// 恰好是同一个 bigint"这种巧合冲突的概率进一步降低（虽然本项目
// 目前只有这一处用到 advisory lock）。
const lockKeySalt = 0x636f6e766f // "convo" 的十六进制近似,纯粹是个容易辨认的常数

func (w *PgLockedWriter) WithConversationLock(ctx context.Context, convID uuid.UUID, fn func(q platform.Querier) error) error {
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for conversation lock: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lock transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()

	lockKey := int64(uuidHigh64(convID)) ^ lockKeySalt

	// pg_advisory_xact_lock 是事务级咨询锁：事务提交或回滚时自动释放，
	// 不需要手动 unlock——这正是选它而不是会话级 pg_advisory_lock 的原因，
	// 少一步"忘记解锁"的失败模式。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("acquire advisory lock for conversation %s: %w", convID, err)
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lock transaction: %w", err)
	}
	return nil
}

// uuidHigh64 取 uuid 的高 8 字节当一个 uint64——advisory lock 的键
// 只需要"同一个会话总是映射到同一个数字、不同会话大概率映射到不同数字"，
// 不需要保留 uuid 的全部 128 位信息，取一半足够满足这个要求。
func uuidHigh64(id uuid.UUID) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(id[i])
	}
	return v
}
