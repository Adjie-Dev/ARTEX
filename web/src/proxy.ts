import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

const AUTH_PAGES = ["/login", "/setup"];

export function proxy(request: NextRequest) {
  // Mock demo: tidak ada login sungguhan, semua halaman diloloskan (auth guard di sisi klien juga akan meloloskan).
  if (process.env.NEXT_PUBLIC_MOCK === "1") return NextResponse.next();

  const { pathname } = request.nextUrl;
  const token = request.cookies.get("artex_token")?.value;
  const isAuthPage = AUTH_PAGES.some((p) => pathname === p || pathname.startsWith(`${p}/`));

  // Belum login → alihkan ke halaman login
  if (!token && !isAuthPage) {
    return NextResponse.redirect(new URL("/login", request.url));
  }

  // Sudah login tapi mengakses halaman login/setup → alihkan ke halaman utama
  if (token && isAuthPage) {
    return NextResponse.redirect(new URL("/function/tasks", request.url));
  }

  return NextResponse.next();
}

export const config = {
  // Melewati rute internal Next.js, rute API, favicon, dan berkas statis di public/ (termasuk gambar, font, dll)
  matcher: ["/((?!_next/static|_next/image|favicon\\.ico|api/|.*\\.(?:png|jpg|jpeg|gif|webp|svg|ico|woff2?|ttf|otf)$).*)"],
};
