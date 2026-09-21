import { Toaster as Sonner, type ToasterProps } from "sonner"
import { CircleCheckIcon, InfoIcon, TriangleAlertIcon, OctagonXIcon, Loader2Icon } from "lucide-react"

import { useTheme } from "@/hooks/useTheme"

// ⚠️ 【本项目对 shadcn 生成代码的偏离，重新生成时会被覆盖】
// 生成出来的原始版本是 `import { useTheme } from "next-themes"`，
// 但本项目没有装 next-themes，主题也不是那个方案管的：
// 深色是写死在 index.html 的 <html class="dark"> 上的，切换由
// src/hooks/useTheme.ts 负责（读的就是这个类名）。改成用它，
// 弹窗的深浅色才和页面其余部分同步，也不用为了一个 Toaster 多装一个包。
// 重新跑 `npx --yes shadcn@latest add sonner` 之后要重新打这个补丁。
const Toaster = ({ ...props }: ToasterProps) => {
  const { theme } = useTheme()

  return (
    <Sonner
      theme={theme as ToasterProps["theme"]}
      className="toaster group"
      icons={{
        success: (
          <CircleCheckIcon className="size-4" />
        ),
        info: (
          <InfoIcon className="size-4" />
        ),
        warning: (
          <TriangleAlertIcon className="size-4" />
        ),
        error: (
          <OctagonXIcon className="size-4" />
        ),
        loading: (
          <Loader2Icon className="size-4 animate-spin" />
        ),
      }}
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius)",
        } as React.CSSProperties
      }
      toastOptions={{
        classNames: {
          toast: "cn-toast",
        },
      }}
      {...props}
    />
  )
}

export { Toaster }
