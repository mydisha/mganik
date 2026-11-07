import { useEffect } from "react";
import { strings } from "./strings";

/** Judul tab unik per halaman: "Stok · mGanik". */
export function useDocumentTitle(title: string) {
  useEffect(() => {
    document.title = `${title} · ${strings.brand}`;
  }, [title]);
}
