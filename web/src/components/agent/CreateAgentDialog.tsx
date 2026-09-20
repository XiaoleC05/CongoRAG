import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ErrorText } from '@/components/ErrorText'
import { useCreateAgent, useToolCatalog } from '@/hooks/useAgents'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * 创建 Agent 的表单弹窗。
 *
 * 【工具勾选列表来自 GET /api/v1/tools,不是硬编码】migrations/0005
 * 的种子数据是这份清单唯一的来源——表单不该自己知道"现在有哪几个
 * 工具",那样加一个新工具就要同时改后端种子数据和前端这个文件。
 */
export function CreateAgentDialog({ open, onOpenChange }: Props) {
  const { data: tools } = useToolCatalog()
  const create = useCreateAgent()

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [instruction, setInstruction] = useState('')
  const [selectedTools, setSelectedTools] = useState<string[]>([])

  const reset = () => {
    setName('')
    setDescription('')
    setInstruction('')
    setSelectedTools([])
    create.reset()
  }

  const toggleTool = (toolName: string, checked: boolean) => {
    setSelectedTools((prev) =>
      checked ? [...prev, toolName] : prev.filter((n) => n !== toolName),
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) reset()
        onOpenChange(next)
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate(
              { name, description, instruction, toolNames: selectedTools },
              {
                onSuccess: () => {
                  reset()
                  onOpenChange(false)
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>创建 Agent</DialogTitle>
            <DialogDescription>
              给它一个名字、系统提示词，选它能用哪些工具。
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="agent-name">名字</Label>
              <Input
                id="agent-name"
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="计算助手"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="agent-description">描述（可选）</Label>
              <Input
                id="agent-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="帮助用户做算术运算"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="agent-instruction">系统提示词（可选）</Label>
              <textarea
                id="agent-instruction"
                value={instruction}
                onChange={(e) => setInstruction(e.target.value)}
                placeholder="你是一个帮助用户做算术计算的助手，遇到需要计算的问题就调用 calculator 工具。"
                rows={3}
                className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 w-full rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px]"
              />
            </div>

            <div className="space-y-1.5">
              <Label>可用工具</Label>
              <div className="space-y-2">
                {tools?.map((tool) => (
                  <label key={tool.name} className="flex items-start gap-2 text-sm">
                    <Checkbox
                      checked={selectedTools.includes(tool.name)}
                      onCheckedChange={(checked) => toggleTool(tool.name, checked === true)}
                    />
                    <span>
                      <span className="font-medium">{tool.name}</span>
                      <span className="text-muted-foreground ml-1.5">{tool.description}</span>
                    </span>
                  </label>
                ))}
              </div>
            </div>

            <ErrorText error={create.error} />
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              取消
            </Button>
            <Button type="submit" disabled={create.isPending || name.trim() === ''}>
              {create.isPending ? '创建中…' : '创建'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
