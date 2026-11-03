import type { ReactNode } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { DialogContent } from "@panasms/ui";
import { translator } from "@panasms/i18n";
const tr = translator("containers");
export function Modal({
  title,
  children,
  footer,
  close,
  busy = false,
  dirty = false,
  compact = false,
  message,
}: {
  message?: string;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  close: () => void;
  busy?: boolean;
  dirty?: boolean;
  compact?: boolean;
}) {
  return (
    <Dialog.Root
      open
      onOpenChange={(v) => {
        if (!v) close();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <DialogContent
          className="settings-dialog containers-dialog"
          aria-describedby={undefined}
          variant={compact ? "compact" : "form"}
          intent={compact ? "confirm" : "edit"}
          dirty={dirty}
          busy={busy}
          message={message ?? tr("submitting")}
          header={<Dialog.Title>{title}</Dialog.Title>}
          footer={
            footer ? <div className="dialog-actions">{footer}</div> : undefined
          }
        >
          {children}
        </DialogContent>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
