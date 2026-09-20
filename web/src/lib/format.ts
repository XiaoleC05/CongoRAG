/**
 * 把后端返回的 ISO 时间串按【浏览器所在时区】显示成 `2026-09-20 00:35`。
 *
 * 【不能用字符串切片】后端返回的是 `2026-09-20T00:35:04.99+08:00`——
 * 那是 api 进程的本地时间。交付期 api 跑在容器里（默认 UTC），
 * 直接切前 16 个字符会把 UTC 时间当本地时间显示，差 8 小时，
 * 而且不会报错。用 Date 解析后按浏览器时区格式化才是对的。
 *
 * 不走 Intl：本地工具站不需要多语言，手动拼可预测、无 locale 差异。
 */
export function formatDateTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso // 解析不了就原样显示，比显示 Invalid Date 好

  const p = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}`
  )
}

/**
 * 把字节数显示成 "12.3 KB" 这种人类可读的形式。
 *
 * 只到 GB——本地单机工具的单个文档不会大到需要 TB 这一档，
 * 真出现那么大的文件，说明别处的校验该拦但没拦住，不该在这里静默显示。
 */
export function formatByteSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let value = bytes / 1024
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  return `${value.toFixed(1)} ${units[i]}`
}
