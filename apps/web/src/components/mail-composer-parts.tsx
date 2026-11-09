import * as React from "react"
import { Clock3 } from "lucide-react"

import {
  defaultScheduledSendValue,
  scheduledSendPresets,
  toDateTimeLocalValue,
} from "@/components/mail-schedule-utils"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useToast } from "@/hooks/use-toast"
import { uiText, useLanguage as useUiLanguage } from "@/lib/language"
import { cn } from "@/lib/utils"

export function ComposeField({
  label,
  children,
  action,
}: {
  label: string
  children: React.ReactNode
  action?: React.ReactNode
}) {
  useUiLanguage()

  return (
    <div className="flex min-h-14 min-w-0 shrink-0 flex-col gap-2 border-b px-4 py-2 sm:flex-row sm:items-center sm:px-6">
      <Label className="shrink-0 text-base font-normal text-foreground sm:w-20">
        {uiText(label)}
      </Label>
      <div className="flex min-w-0 flex-col gap-2 sm:flex-1 sm:flex-row sm:items-center">
        {children}
        {action}
      </div>
    </div>
  )
}

export function ScheduleSendDialog({
  open,
  pending,
  onOpenChange,
  onConfirm,
}: {
  open: boolean
  pending: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: (sendAt: string) => void
}) {
  useUiLanguage()

  const [value, setValue] = React.useState("")
  const { toast } = useToast()
  const presets = React.useMemo(() => (open ? scheduledSendPresets() : []), [open])

  React.useEffect(() => {
    if (open) setValue(defaultScheduledSendValue())
  }, [open])

  function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const date = new Date(value)
    if (!value || Number.isNaN(date.getTime()) || !date.getTime()) {
      toast({ title: "请选择发送时间" })
      return
    }
    if (date.getTime() <= Date.now() + 30_000) {
      toast({ title: "发送时间需要晚于当前时间" })
      return
    }
    onConfirm(date.toISOString())
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form className="grid gap-4" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{uiText("定时发送")}</DialogTitle>
          </DialogHeader>
          <div className="grid grid-cols-2 gap-2">
            {presets.map((preset) => (
              <Button
                type="button"
                key={preset.label}
                variant={value === preset.value ? "secondary" : "outline"}
                className="justify-start font-normal"
                onClick={() => setValue(preset.value)}
              >
                <Clock3 className="h-4 w-4" />
                {uiText(preset.label)}
              </Button>
            ))}
          </div>
          <div className="grid gap-2">
            <Label htmlFor="schedule-send-at">{uiText("发送时间")}</Label>
            <Input
              id="schedule-send-at"
              type="datetime-local"
              value={value}
              min={toDateTimeLocalValue(new Date(Date.now() + 60_000))}
              onChange={(event) => setValue(event.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={pending}
            >
              {uiText("取消")}
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? uiText("正在设置...") : uiText("确认定时")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function ToolbarTextButton({
  label,
  icon,
  active,
  disabled,
  onClick,
}: {
  label: string
  icon: React.ReactNode
  active?: boolean
  disabled?: boolean
  onClick?: () => void
}) {
  useUiLanguage()

  return (
    <Button
      type="button"
      variant={active ? "secondary" : "ghost"}
      size="sm"
      className={cn(
        "h-8 gap-1.5 rounded-lg px-2 font-normal transition-all hover:bg-accent hover:text-foreground hover:shadow-sm",
        active && "border border-primary/30 bg-primary/10 text-primary shadow-sm"
      )}
      title={uiText(label)}
      aria-label={uiText(label)}
      aria-pressed={active || undefined}
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
      disabled={disabled}
    >
      {icon}
      {uiText(label)}
    </Button>
  )
}
