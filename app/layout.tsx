import type { Metadata } from "next";
import "./globals.css";
import { SuiteFrame } from "@/components/suite-frame";
export const metadata: Metadata = { title: "MarketLane", description: "MarketLane business workspace.", icons: { icon: "/favicon.svg", shortcut: "/favicon.svg" } };
export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) { return <html lang="en"><body className="antialiased min-h-screen bg-stone-50 text-stone-900 flex flex-col font-sans"><SuiteFrame>{children}</SuiteFrame></body></html>; }
