<script setup lang="ts">
import { computed, defineAsyncComponent, type Component } from "vue";
import AsyncErrorPanel from "@/components/common/AsyncErrorPanel.vue";
import type { FileItem } from "@/api/types";
import type { ActiveFilePreview, FilePreviewKind } from "./filePreview";

// 预览组件为异步 chunk，加载失败时落到错误兜底，避免预览区静默空白。
const asyncPreview = (loader: () => Promise<Component>) =>
  defineAsyncComponent({ loader, errorComponent: AsyncErrorPanel });

const props = defineProps<{
  accountId: number;
  files: FileItem[];
  active: ActiveFilePreview;
}>();

const emit = defineEmits<{
  close: [];
  download: [file: FileItem];
}>();

const previewComponents = {
  video: asyncPreview(() => import("./VideoPreview.vue")),
  audio: asyncPreview(() => import("./AudioPreview.vue")),
  image: asyncPreview(() => import("./ImagePreview.vue")),
  text: asyncPreview(() => import("./TextPreview.vue")),
  pdf: asyncPreview(() => import("./PdfPreview.vue")),
  docx: asyncPreview(() => import("./DocxPreview.vue")),
  spreadsheet: asyncPreview(() => import("./SpreadsheetPreview.vue")),
  archive: asyncPreview(() => import("./ArchivePreview.vue")),
  pptx: asyncPreview(() => import("./PptxPreview.vue")),
} satisfies Record<FilePreviewKind, ReturnType<typeof defineAsyncComponent>>;

const mediaPreviewKinds = new Set<FilePreviewKind>(["video", "audio", "image"]);
const activeComponent = computed(() => previewComponents[props.active.kind]);
const activeProps = computed(() =>
  mediaPreviewKinds.has(props.active.kind)
    ? { files: props.files, initialFileId: props.active.file.id }
    : { file: props.active.file },
);

function forwardDownload(file: FileItem) {
  emit("download", file);
}
</script>

<template>
  <component
    :is="activeComponent"
    :account-id="accountId"
    v-bind="activeProps"
    @close="emit('close')"
    @download="forwardDownload"
  />
</template>
