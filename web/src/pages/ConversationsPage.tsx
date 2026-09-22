import { MessagesSquare, MoreHorizontal, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'

import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import {
  useConversations,
  useCreateConversation,
  useDeleteConversation,
} from '@/hooks/useConversations'
import { useErrorToast } from '@/hooks/useErrorToast'
import { errorPresentation } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { flattenPages } from '@/lib/pagination'

type Conversation = Schemas['Conversation']

/**
 * 交给弹窗内联显示的那部分错误。
 *
 * 【为什么要过滤】删除失败后有一条已经由 useErrorToast 说过了（能重试的
 * 那类），弹窗里再渲染一份就是同一个错误两个 role="alert"，读屏软件念两遍。
 * 剩下的（比如这条会话已经不存在了）必须留在弹窗里——弹窗还开着，
 * 错误就该贴在用户刚才点的地方。
 */
const residual = (err: unknown) =>
  err && errorPresentation(err, 'mutation') !== 'toast' ? err : undefined

/**
 * 会话列表页（issue #78）。
 *
 * 【为什么是单独一页，而不是侧栏里的一列】
 * 界面引用方案 §2.2 / §2.6 要的是 ChatGPT 式布局：左边一列会话、右边消息区，
 * 且"会话列表为主体"。要做到那个形态有两条路，两条都走不通：
 *
 *   1. 塞进 AppLayout 的侧栏 —— AppLayout.tsx 不在本批次允许改动的文件清单里，
 *      而且侧栏现在是"导航 + 主题切换"的固定结构，把可滚动的会话列表塞进
 *      导航组会把知识库/Agent 那两个入口挤下去。
 *   2. 在 `conversations/:id` 外面套一层布局路由（左列表 + 右 <Outlet/>）——
 *      那需要新增一个 layouts/ 下的壳文件，同样在清单之外。
 *
 * 所以本批次只交付"能列出、能切、能新建、能删"这一半，做成一个独立页面，
 * 顺带修好一件事：侧栏「对话」这个入口在它之前指向 `/conversations`，
 * 而 router.tsx 里只有 `conversations/:id`——点它得到的是 404。
 *
 * 【"当前会话高亮"为什么没有】高亮的对象必须和列表同屏才谈得上"当前"。
 * 独立成一页之后，这一页上没有"正在看的那一个会话"。侧栏「对话」那一项
 * 的整段高亮（pathname.startsWith('/conversations')）已经能回答"你在不在
 * 对话这一块"，但回答不了"是哪一个"。这一条要等上面第 2 条路落地。
 */
export default function ConversationsPage() {
  const { data, isPending, error, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useConversations()
  const createConversation = useCreateConversation()
  const removeConversation = useDeleteConversation()
  const navigate = useNavigate()
  const showError = useErrorToast()

  const conversations = flattenPages(data)

  // 待删除的目标。null = 弹窗关闭。和知识库列表页同一个做法：弹窗状态放在
  // 页面这一层，行组件只负责"请求打开"。
  const [deleting, setDeleting] = useState<Conversation | null>(null)

  // 【新建的时候为什么要带一个标题】契约里 title 是可选的，服务端也允许
  // 空标题（handler 注释写明"允许空 body"）。但空标题在列表里就是一行
  // 什么都没有的条目——用户分不清它是哪一个。给一个占位名字，比让他面对
  // 一行空白好。真正有意义的名字应该来自第一次提问（后端还没做这件事，
  // 做了之后这里就不用管了）。
  const handleCreate = () => {
    createConversation.mutate(
      { title: '新会话' },
      {
        onSuccess: (conv) => navigate(`/conversations/${conv.id}`),
        onError: showError,
      },
    )
  }

  // 打开确认框前先清掉上一次的错误，否则重开时会看到已经过期的报错（§7）。
  const openDelete = (conv: Conversation) => {
    removeConversation.reset()
    setDeleting(conv)
  }

  return (
    <div className="mx-auto max-w-3xl p-6">
      <header className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold">对话</h1>
        <Button onClick={handleCreate} disabled={createConversation.isPending}>
          <Plus />
          {createConversation.isPending ? '创建中…' : '新建会话'}
        </Button>
      </header>

      {/* 写操作失败里"toast 说不清"的那部分（比如参数被拒）留在页面上。
          能重试的已经由 useErrorToast 说过了，不重复渲染。 */}
      <ErrorText error={residual(createConversation.error)} className="mb-4" />

      {isPending ? (
        <SkeletonList />
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : conversations.length === 0 ? (
        <EmptyState onCreate={handleCreate} pending={createConversation.isPending} />
      ) : (
        <div className="border-border divide-border divide-y overflow-hidden rounded-xl border">
          {conversations.map((conv) => (
            // 【Link 和操作按钮是兄弟，不是父子】把 DropdownMenu 塞进 Link
            // 里会得到"链接里套按钮"的非法结构：点菜单会同时触发导航，
            // 而且读屏软件读不出这两个控件的关系。所以整行是一个 flex
            // 容器，左边那个 Link 撑满剩余宽度（min-w-0 + flex-1，
            // 否则长标题会把按钮挤出去）。
            <div key={conv.id} className="flex items-center gap-2">
              <Link
                to={`/conversations/${conv.id}`}
                className="hover:bg-muted/50 focus-visible:bg-muted/50 flex min-w-0 flex-1 items-center justify-between gap-4 px-4 py-3 outline-none"
              >
                {/* 【标题为空时给一句兜底】后端允许空标题（见上面的注释），
                    直接渲染会是一条看不见文字的空白行——看起来像渲染坏了。 */}
                <span className="truncate font-medium">{conv.title || '未命名会话'}</span>
                {/* 【显示的是"最近活动时间"】契约里列表就是按它倒序的，
                    显示 createdAt 会和排序自相矛盾（一个三天前建的、刚刚才用过
                    的会话排在第一条，旁边的日期却是三天前）。 */}
                <span className="text-muted-foreground shrink-0 text-xs">
                  {formatDateTime(conv.updatedAt)}
                </span>
              </Link>

              {/* 【每行最多一个主操作，其余进溢出菜单（§19）】这一行现在
                  只有一个次要操作（删除），但它之后会长（重命名之类），
                  现在就用菜单形态是为了那一天的代价是零。形状照
                  DocumentRowActions。可访问名带上对象名（§14）：
                  一串"删除"里必须听得出是哪一条。 */}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="mr-2 shrink-0"
                    aria-label={`${conv.title || '未命名会话'} 的操作`}
                    disabled={removeConversation.isPending}
                  >
                    <MoreHorizontal className="text-muted-foreground" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="min-w-40">
                  <DropdownMenuItem variant="destructive" onSelect={() => openDelete(conv)}>
                    <Trash2 />
                    删除
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          ))}

          {/* 【加载更多放在列表下方】列表按最近活动时间倒序，更旧的在下面。
              hasNextPage 为假时整个不渲染，而不是渲染成禁用——禁用会让用户
              以为"等一下就有了"（§16）。 */}
          {hasNextPage && (
            <div className="flex justify-center py-2">
              <Button
                variant="ghost"
                size="sm"
                disabled={isFetchingNextPage}
                onClick={() => void fetchNextPage()}
              >
                {isFetchingNextPage ? '加载中…' : '加载更多'}
              </Button>
            </div>
          )}
        </div>
      )}

      <DeleteConversationDialog
        target={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        pending={removeConversation.isPending}
        error={residual(removeConversation.error)}
        onConfirm={() => {
          if (!deleting) return
          removeConversation.mutate(deleting.id, {
            onSuccess: () => setDeleting(null),
            onError: showError,
          })
        }}
      />
    </div>
  )
}

type DeleteDialogProps = {
  /** null 表示没有待删除的目标，弹窗关闭 */
  target: Conversation | null
  onOpenChange: (open: boolean) => void
  pending: boolean
  error: unknown
  onConfirm: () => void
}

/**
 * 删除会话的确认框。
 *
 * 【为什么是 AlertDialog 而不是 Dialog】这是破坏性且**不可逆**的操作
 * （§20）：契约写明是级联硬删，消息、事件、摘要一起走，删了拉不回来——
 * 包括其中的回答与引用。AlertDialog 的语义（role="alertdialog"、默认
 * 聚焦在取消上）正对这种场景。
 *
 * 【为什么就放在页面文件里，不抽到 components/conversation/】它只有一个
 * 调用方，而 README 的判据是"等第二个页面真的要用了再抽"。它按域应该
 * 归 components/conversation/，但那个目录不在本批次的改动清单里；
 * 抽过去是纯搬家，等第二个调用方出现时连搬家带复用一起做。
 *
 * 【确认按钮的文案是"确定"】和 DeleteKnowledgeDialog 一致：红色已经
 * 表达了危险性，按钮再重复一遍动词反而啰嗦。
 */
function DeleteConversationDialog({
  target,
  onOpenChange,
  pending,
  error,
  onConfirm,
}: DeleteDialogProps) {
  return (
    <AlertDialog open={!!target} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除「{target?.title || '未命名会话'}」？</AlertDialogTitle>
          <AlertDialogDescription>
            此操作不可撤销，这个会话里的消息、事件和摘要会一并删除，
            包括其中的回答与引用。
          </AlertDialogDescription>
        </AlertDialogHeader>

        <ErrorText error={error} />

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>取消</AlertDialogCancel>
          <AlertDialogAction
            disabled={pending}
            onClick={(e) => {
              // 不阻止的话 AlertDialog 会立刻关闭，请求还在飞
              e.preventDefault()
              onConfirm()
            }}
          >
            {pending ? '删除中…' : '确定'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function SkeletonList() {
  return (
    <div className="space-y-2">
      {Array.from({ length: 5 }, (_, i) => (
        <Skeleton key={i} className="h-12 rounded-lg" />
      ))}
    </div>
  )
}

/** 一条会话都没有。出口有两个：新建一个，或者去知识库那边带着文档开一个。 */
function EmptyState({ onCreate, pending }: { onCreate: () => void; pending: boolean }) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <MessagesSquare className="text-muted-foreground mb-3 size-8" />
      <p className="font-medium">还没有会话</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        新建一个空会话，或者从知识库详情页点「开始对话」——
        后者会把那个知识库挂上来，回答时带着检索结果。
      </p>
      <Button onClick={onCreate} disabled={pending}>
        <Plus />
        新建会话
      </Button>
    </div>
  )
}
