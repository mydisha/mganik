import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import { errorMessage } from "@/shared/lib/api";
import { Button } from "@/shared/ui/button";
import { logout } from "./api";
import { useSession } from "./session";
import { authStrings as t } from "./strings";

/** Nama, email, dan peran pengguna, plus tombol keluar. */
export function AccountMenu() {
  const me = useSession();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const roles = me.roles.map((role) => role.name).join(", ");

  const signOut = useMutation({
    mutationFn: logout,
    onSuccess: () => {
      queryClient.clear();
      toast.success(t.account.signedOut);
      navigate("/login", { replace: true });
    },
    // Gagal keluar: tetap masuk, beri tahu alasannya.
    onError: (error) => toast.error(errorMessage(error)),
  });

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button variant="ghost" aria-label={`${t.account.menu}: ${me.user.name}`} className="max-w-48 sm:max-w-64">
          <span className="truncate">{me.user.name}</span>
          <ChevronDown aria-hidden="true" />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={6}
          className="z-50 w-64 max-w-[calc(100vw-2rem)] rounded-lg border bg-popover p-1 text-popover-foreground shadow-md"
        >
          <div className="space-y-0.5 px-3 py-2">
            <p className="truncate text-sm font-medium">{me.user.name}</p>
            <p className="truncate text-xs text-muted-foreground">{me.user.email}</p>
            {roles && <p className="text-xs text-muted-foreground">{roles}</p>}
          </div>
          <DropdownMenu.Separator className="my-1 h-px bg-border" />
          <DropdownMenu.Item
            disabled={signOut.isPending}
            onSelect={(event) => {
              event.preventDefault();
              signOut.mutate();
            }}
            className="flex min-h-9 items-center rounded-md px-3 text-sm select-none data-[disabled]:opacity-50 data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground max-md:min-h-11"
          >
            {signOut.isPending ? t.account.signingOut : t.account.signOut}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
