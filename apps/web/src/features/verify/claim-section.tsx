import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorMessage, errorStatus } from "@/shared/lib/api";
import { formatDate } from "@/shared/lib/format";
import { strings } from "@/shared/lib/strings";
import { Alert } from "@/shared/ui/alert";
import { Button } from "@/shared/ui/button";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";
import { Skeleton } from "@/shared/ui/skeleton";
import { Spinner } from "@/shared/ui/spinner";
import {
  type ClaimResponse,
  claimPoints,
  consumerKeys,
  fetchBalance,
  fetchConsumer,
  type GeoPoint,
  logoutConsumer,
  requestOtp,
  verifyOtp,
} from "./api";
import { normalizePhone, normalizePin } from "./input";
import { verifyStrings as t } from "./strings";

const c = t.claim;

/** Login konsumen dengan nomor HP: minta kode, lalu masukkan kode. */
function OtpLogin() {
  const queryClient = useQueryClient();
  const [phoneInput, setPhoneInput] = useState("");
  const [phone, setPhone] = useState<string | null>(null);
  const [phoneError, setPhoneError] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [codeError, setCodeError] = useState<string | null>(null);

  const send = useMutation({ mutationFn: requestOtp });
  const verify = useMutation({
    mutationFn: ({ to, otp }: { to: string; otp: string }) => verifyOtp(to, otp),
    onSuccess: (me) => {
      queryClient.setQueryData(consumerKeys.me, me);
      void queryClient.invalidateQueries({ queryKey: consumerKeys.balance });
    },
  });

  function submitPhone(event: FormEvent) {
    event.preventDefault();
    const normalized = normalizePhone(phoneInput);
    if (!normalized) {
      setPhoneError(c.phoneInvalid);
      return;
    }
    setPhoneError(null);
    send.mutate(normalized, { onSuccess: () => setPhone(normalized) });
  }

  function submitCode(event: FormEvent) {
    event.preventDefault();
    if (!phone) return;
    const otp = code.trim();
    if (!/^[0-9]{6}$/.test(otp)) {
      setCodeError(c.otpInvalid);
      return;
    }
    setCodeError(null);
    verify.mutate({ to: phone, otp });
  }

  if (!phone) {
    return (
      <form noValidate onSubmit={submitPhone} className="flex flex-col gap-4">
        {send.isError && <Alert tone="danger">{errorMessage(send.error)}</Alert>}
        <FormField id="claim-phone" label={c.phone} hint={c.phoneHint} error={phoneError ?? undefined}>
          {(control) => (
            <Input
              {...control}
              type="tel"
              inputMode="tel"
              autoComplete="tel"
              value={phoneInput}
              onChange={(e) => setPhoneInput(e.target.value)}
              disabled={send.isPending}
            />
          )}
        </FormField>
        <Button type="submit" disabled={send.isPending} className="self-start">
          {send.isPending && <Spinner />}
          {send.isPending ? c.sendingCode : c.sendCode}
        </Button>
      </form>
    );
  }

  return (
    <form noValidate onSubmit={submitCode} className="flex flex-col gap-4">
      {send.data && <Alert tone="info">{c.codeSent(phone, Math.max(1, Math.round(send.data.expires_in_seconds / 60)))}</Alert>}
      {verify.isError && <Alert tone="danger">{errorMessage(verify.error)}</Alert>}
      {send.isError && <Alert tone="danger">{errorMessage(send.error)}</Alert>}
      <FormField id="claim-otp" label={c.otp} hint={c.otpHint} error={codeError ?? undefined}>
        {(control) => (
          <Input
            {...control}
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            disabled={verify.isPending}
            className="font-mono tracking-widest"
          />
        )}
      </FormField>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={verify.isPending}>
          {verify.isPending && <Spinner />}
          {verify.isPending ? c.verifying : c.verify}
        </Button>
        <Button variant="outline" onClick={() => send.mutate(phone)} disabled={send.isPending || verify.isPending}>
          {c.resend}
        </Button>
        <Button
          variant="ghost"
          onClick={() => {
            setPhone(null);
            setCode("");
            verify.reset();
            send.reset();
          }}
          disabled={verify.isPending}
        >
          {c.changePhone}
        </Button>
      </div>
    </form>
  );
}

function claimOutcome(claim: ClaimResponse, now: Date): { tone: "success" | "info" | "danger"; text: string } {
  const prefix = claim.replayed ? `${c.replayed} ` : "";
  switch (claim.status) {
    case "APPROVED":
      return { tone: "success", text: prefix + c.approved(claim.points) };
    case "PENDING":
      // Kontrak tidak membedakan masa tunggu dan review: masa tunggu selalu
      // berakhir di masa depan, review memakai tanggal klaim.
      return new Date(claim.available_at) > now
        ? { tone: "info", text: prefix + c.pendingUntil(claim.points, formatDate(claim.available_at)) }
        : { tone: "info", text: prefix + c.review(claim.points) };
    case "REJECTED":
    case "REVERSED":
      return { tone: "danger", text: prefix + c.rejected };
  }
}

function PinClaim({ code, geo }: { code: string; geo: GeoPoint | null }) {
  const queryClient = useQueryClient();
  const [pin, setPin] = useState("");
  const [pinError, setPinError] = useState<string | null>(null);
  const claim = useMutation({
    mutationFn: (value: string) => claimPoints(code, value, geo),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: consumerKeys.balance }),
    onError: (error) => {
      if (errorStatus(error) === 401) void queryClient.invalidateQueries({ queryKey: consumerKeys.me });
    },
  });
  const locked = errorStatus(claim.error) === 423;
  const wrongPin = errorStatus(claim.error) === 422;

  function submit(event: FormEvent) {
    event.preventDefault();
    const value = normalizePin(pin);
    if (!value) {
      setPinError(c.pinInvalid);
      return;
    }
    setPinError(null);
    claim.mutate(value);
  }

  if (claim.isSuccess) {
    const outcome = claimOutcome(claim.data, new Date());
    return <Alert tone={outcome.tone}>{outcome.text}</Alert>;
  }

  return (
    <form noValidate onSubmit={submit} className="flex flex-col gap-4">
      {claim.isError && !wrongPin && <Alert tone="danger">{errorMessage(claim.error)}</Alert>}
      <FormField
        id="claim-pin"
        label={c.pin}
        hint={c.pinHint}
        error={pinError ?? (wrongPin ? errorMessage(claim.error) : undefined)}
      >
        {(control) => (
          <Input
            {...control}
            autoComplete="off"
            autoCapitalize="characters"
            spellCheck={false}
            maxLength={20}
            value={pin}
            onChange={(e) => setPin(e.target.value)}
            disabled={claim.isPending || locked}
            className="font-mono tracking-wider"
          />
        )}
      </FormField>
      <Button type="submit" disabled={claim.isPending || locked} className="self-start">
        {claim.isPending && <Spinner />}
        {claim.isPending ? c.submitting : c.submit}
      </Button>
    </form>
  );
}

function Balance() {
  const balance = useQuery({ queryKey: consumerKeys.balance, queryFn: fetchBalance, retry: false });
  if (balance.isPending) return <p className="text-sm text-muted-foreground">{c.balanceLoading}</p>;
  if (balance.isError) return <p className="text-sm text-muted-foreground">{c.balanceFailed}</p>;
  return <p className="text-sm font-medium">{c.balance(balance.data.available, balance.data.pending)}</p>;
}

/** Klaim poin setelah produk dinyatakan asli. */
export function ClaimSection({ code, geo }: { code: string; geo: GeoPoint | null }) {
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: consumerKeys.me, queryFn: fetchConsumer, retry: false });
  const signOut = useMutation({
    mutationFn: logoutConsumer,
    onSuccess: () => queryClient.removeQueries({ queryKey: ["consumer"] }),
  });

  return (
    <section aria-labelledby="klaim-poin" className="flex flex-col gap-4 rounded-lg border bg-card p-4 shadow-xs">
      <div className="space-y-1">
        <h2 id="klaim-poin" className="text-base font-semibold">
          {c.title}
        </h2>
        <p className="text-sm text-muted-foreground">{c.description}</p>
      </div>
      {me.isPending ? (
        <div className="space-y-3" aria-hidden="true">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-11 w-full" />
        </div>
      ) : me.isError && errorStatus(me.error) !== 401 ? (
        <div className="flex flex-col items-start gap-3">
          <Alert tone="danger">{errorMessage(me.error)}</Alert>
          <Button variant="outline" onClick={() => void me.refetch()}>
            {strings.common.retry}
          </Button>
        </div>
      ) : me.isError ? (
        <OtpLogin />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm">{c.signedInAs(me.data.phone_masked)}</p>
            <Button variant="ghost" size="sm" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
              {c.signOut}
            </Button>
          </div>
          {signOut.isError && <Alert tone="danger">{errorMessage(signOut.error)}</Alert>}
          <Balance />
          <PinClaim code={code} geo={geo} />
        </>
      )}
    </section>
  );
}
