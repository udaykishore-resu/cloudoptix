import type { Metadata } from "next";
import "./globals.css";
import { Providers } from "./providers";

// Deliberately not using next/font/google here: this app must build in
// network-restricted environments (no egress to fonts.googleapis.com), so
// typography is a system-font stack defined in globals.css's --font-sans /
// --font-mono tokens (which do list "Inter" / "JetBrains Mono" first for
// environments where those are installed, falling back gracefully otherwise).

export const metadata: Metadata = {
  title: {
    default: "CloudOptix",
    template: "%s · CloudOptix",
  },
  description: "Cloud architecture-economics platform: cost intelligence, architecture economics and safe, governed autonomous optimization for AWS.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="font-sans antialiased">
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
