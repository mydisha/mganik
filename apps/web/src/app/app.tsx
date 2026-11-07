import { useState } from "react";
import { RouterProvider } from "react-router";
import { AppProviders, createQueryClient } from "./providers";
import { createAppRouter } from "./routes";

export function App() {
  const [router] = useState(createAppRouter);
  const [queryClient] = useState(() => createQueryClient(router));

  return (
    <AppProviders client={queryClient}>
      <RouterProvider router={router} />
    </AppProviders>
  );
}
