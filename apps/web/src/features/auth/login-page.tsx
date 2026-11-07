import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useForm } from "react-hook-form";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { z } from "zod";
import { errorMessage, fieldErrors, ResponseError } from "@/shared/lib/api";
import { codePointLength } from "@/shared/lib/format";
import { strings } from "@/shared/lib/strings";
import { useDocumentTitle } from "@/shared/lib/use-document-title";
import { Alert } from "@/shared/ui/alert";
import { Button } from "@/shared/ui/button";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";
import { Skeleton } from "@/shared/ui/skeleton";
import { Spinner } from "@/shared/ui/spinner";
import { login, meQueryKey } from "./api";
import { useMeQuery } from "./session";
import { authStrings as t } from "./strings";

const schema = z.object({
  email: z
    .string()
    .trim()
    .min(1, t.login.emailRequired)
    .max(254, t.login.emailInvalid)
    .regex(/^[^\s@]+@[^\s@]+\.[^\s@]+$/u, t.login.emailInvalid),
  // Password tidak dipangkas: spasi di tepi bisa jadi bagian password.
  password: z
    .string()
    .min(1, t.login.passwordRequired)
    .refine((v) => codePointLength(v) <= 1024, t.login.passwordRequired),
});

type LoginForm = z.infer<typeof schema>;

/** Pesan gagal masuk. 429 memakai menit dari server, atau dari Retry-After. */
function loginFailure(error: unknown): string {
  if (error instanceof ResponseError && error.status === 429 && !error.problem?.detail && error.retryAfterSeconds !== null) {
    return t.login.tooManyAttempts(Math.max(1, Math.ceil(error.retryAfterSeconds / 60)));
  }
  return errorMessage(error);
}

function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-sm flex-col justify-center gap-6 px-4 py-10">
      <p className="text-lg font-semibold">{strings.brand}</p>
      {children}
    </main>
  );
}

export function LoginPage() {
  const me = useMeQuery();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const form = useForm<LoginForm>({ resolver: zodResolver(schema), defaultValues: { email: "", password: "" } });
  useDocumentTitle(t.login.title);

  const submit = useMutation({
    mutationFn: login,
    onSuccess: (profile) => {
      queryClient.setQueryData(meQueryKey, profile);
      navigate("/console", { replace: true });
    },
    onError: (error) => {
      for (const [field, message] of Object.entries(fieldErrors(error))) {
        if (field === "email" || field === "password") form.setError(field, { message });
      }
    },
  });

  if (me.isPending) {
    return (
      <AuthLayout>
        <div className="space-y-4 rounded-lg border bg-card p-5" aria-busy="true">
          <span className="sr-only">{t.login.checking}</span>
          <Skeleton className="h-6 w-3/4" />
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-24" />
        </div>
      </AuthLayout>
    );
  }
  if (me.isSuccess) return <Navigate to="/console" replace />;

  const expired = params.get("reason") === "session_expired";
  const errors = form.formState.errors;
  const pending = submit.isPending;

  return (
    <AuthLayout>
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t.login.title}</h1>
        <p className="text-sm text-muted-foreground">{t.login.description}</p>
      </div>
      {expired && !submit.isError && <Alert tone="warning">{t.login.sessionExpired}</Alert>}
      {submit.isError && <Alert tone="danger">{loginFailure(submit.error)}</Alert>}
      <form
        noValidate
        onSubmit={form.handleSubmit((values) => submit.mutate(values))}
        className="flex flex-col gap-4 rounded-lg border bg-card p-5 shadow-xs"
      >
        <FormField id="login-email" label={t.login.email} error={errors.email?.message}>
          {(control) => (
            <Input
              {...control}
              type="email"
              autoComplete="username"
              inputMode="email"
              disabled={pending}
              {...form.register("email")}
            />
          )}
        </FormField>
        <FormField id="login-password" label={t.login.password} error={errors.password?.message}>
          {(control) => (
            <Input {...control} type="password" autoComplete="current-password" disabled={pending} {...form.register("password")} />
          )}
        </FormField>
        <Button type="submit" disabled={pending} className="self-start">
          {pending && <Spinner />}
          {pending ? t.login.submitting : t.login.submit}
        </Button>
      </form>
    </AuthLayout>
  );
}
