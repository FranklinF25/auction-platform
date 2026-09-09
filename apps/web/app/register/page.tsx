"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { inputClass, labelClass, primaryButtonClass } from "@/components/formStyles";
import { api } from "@/lib/api";

export default function RegisterPage() {
  const router = useRouter();
  const queryClient = useQueryClient();

  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const register = useMutation({
    mutationFn: () =>
      api.register({ name: name.trim(), email: email.trim(), password }),
    onSuccess: (user) => {
      queryClient.setQueryData(["me"], user);
      queryClient.invalidateQueries({ queryKey: ["auctions"] });
      router.push("/");
    },
    onError: (err) => setError(err.message),
  });

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    register.mutate();
  }

  const passwordTooShort =
    password.length > 0 && password.length < 8 ? (
      <span className="text-xs font-medium text-zinc-500">
        At least 8 characters.
      </span>
    ) : null;

  return (
    <div className="mx-auto w-full max-w-md">
      <div className="rounded-2xl border border-zinc-200 bg-white p-8 shadow-sm">
        <h1 className="text-2xl font-bold text-zinc-900">Create an account</h1>
        <p className="mt-1 text-sm text-zinc-500">
          Already registered?{" "}
          <Link
            href="/login"
            className="font-medium text-indigo-600 hover:underline"
          >
            Sign in
          </Link>
        </p>

        <form onSubmit={onSubmit} className="mt-6 flex flex-col gap-4">
          {error ? (
            <p
              role="alert"
              className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700"
            >
              {error}
            </p>
          ) : null}

          <label className={labelClass}>
            Name
            <input
              type="text"
              required
              maxLength={100}
              value={name}
              onChange={(e) => setName(e.target.value)}
              className={inputClass}
              placeholder="Ada Lovelace"
              autoComplete="name"
            />
          </label>

          <label className={labelClass}>
            Email
            <input
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className={inputClass}
              placeholder="you@example.com"
              autoComplete="email"
            />
          </label>

          <label className={labelClass}>
            Password
            <input
              type="password"
              required
              minLength={8}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className={inputClass}
              placeholder="At least 8 characters"
              autoComplete="new-password"
            />
            {passwordTooShort}
          </label>

          <button
            type="submit"
            disabled={register.isPending || !name.trim() || !email.trim() || password.length < 8}
            className={primaryButtonClass}
          >
            {register.isPending ? "Creating account…" : "Create account"}
          </button>
        </form>
      </div>
    </div>
  );
}
