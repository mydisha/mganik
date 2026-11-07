import { useInfiniteQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { errorMessage } from "@/shared/lib/api";
import { strings } from "@/shared/lib/strings";
import type { CursorPage } from "./api";

/**
 * Daftar keyset yang dibaca dari atas ke bawah: halaman berikutnya ditambahkan
 * lewat "Muat lebih banyak". Gagal memuat halaman berikutnya menyimpan baris
 * yang sudah ada dan memberi toast; hanya halaman pertama yang memakai ErrorState.
 */
export function useCursorList<P extends CursorPage<unknown>>(
  queryKey: readonly unknown[],
  fetchPage: (cursor: string | undefined) => Promise<P>,
) {
  type T = P["items"][number];
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam }) => fetchPage(pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

  const rows: T[] = query.data?.pages.flatMap((page): T[] => page.items) ?? [];

  async function loadNext() {
    const result = await query.fetchNextPage();
    if (result.isError) toast.error(`${strings.common.loadMoreFailed} ${errorMessage(result.error)}`);
  }

  return {
    query,
    rows,
    firstPage: query.data?.pages[0],
    loadMore: { hasMore: query.hasNextPage, loading: query.isFetchingNextPage, onLoadMore: () => void loadNext() },
  };
}
