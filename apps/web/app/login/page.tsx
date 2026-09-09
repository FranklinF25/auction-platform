"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import {
  inputClass,
  labelClass,
  primaryButtonClass,
} from "@/components/formStyles";
import { api } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const queryClient = useQueryClient();

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const login = useMutation({
    mutationFn: () => api.login({ email: email.trim(), password }),
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
    login.mutate();
  }

  return (
    <div className="mx-auto w-full max-w-md">
      <div className="rounded-2xl border border-zinc-200 bg-white p-8 shadow-sm">
        <h1 className="text-2xl font-bold text-zinc-900">Sign in</h1>
        <p className="mt-1 text-sm text-zinc-500">
          New here?{" "}
          <Link
            href="/register"
            className="font-medium text-indigo-600 hover:underline"
          >
            Create an account
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
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className={inputClass}
              placeholder="••••••••"
              autoComplete="current-password"
            />
          </label>

          <button
            type="submit"
            disabled={login.isPending || !email.trim() || !password}
            className={primaryButtonClass}
          >
            {login.isPending ? "Signing in…" : "Sign in"}
          </button>
        </form>
      </div>
    </div>
  );
}
