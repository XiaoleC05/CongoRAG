/**
 * 知识库名字的长度上限。
 *
 * 【这个数字有三处必须一致】没有代码生成能把它从契约带到 TS，只能人工同步：
 *
 *   1. internal/knowledge/usecase.go  的 maxNameLen（真正的强制点）
 *   2. contracts/openapi.yaml         的 maxLength（给客户端看的声明）
 *   3. 这里                             （提交前的客户端校验）
 *
 * 客户端校验只是省一次白跑的往返，**真正的强制在后端**。
 * 前端漏了不会出安全问题，后端漏了才会。
 */
export const MAX_NAME_LEN = 200

/** 表单提交前的校验。返回 null 表示通过。 */
export function validateName(raw: string): string | null {
  const name = raw.trim()
  if (name === '') return '名字不能为空'
  // 按字符数算，不是字节数——中文一个字算一个
  if ([...name].length > MAX_NAME_LEN) return `名字不能超过 ${MAX_NAME_LEN} 个字`
  return null
}
