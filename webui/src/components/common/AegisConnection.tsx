import { Box, Flex, Text } from "@chakra-ui/react";
import { formatTime } from "../../lib/formatting";
import * as m from "../../paraglide/messages";
import { type AegisStatus, useAegisStatus } from "../../state/aegis";
import { Tooltip } from "../ui/tooltip";

const presentation: Record<
  AegisStatus["state"],
  { label: () => string; color: string }
> = {
  connected: { label: m.aegis_status_connected, color: "green.400" },
  connecting: { label: m.aegis_status_connecting, color: "blue.300" },
  retrying: { label: m.aegis_status_retrying, color: "orange.400" },
  stopped: { label: m.aegis_status_stopped, color: "red.400" },
  disabled: { label: m.aegis_status_disabled, color: "gray.400" },
};

/** The tooltip: the last report that went through, what waits, and why it stopped. */
export const aegisStatusDetails = (status: AegisStatus) => {
  const details = m.aegis_status_details({
    // RFC 3339; formatTime reads a string as epoch milliseconds.
    lastReport: status.lastSuccessAt
      ? formatTime(new Date(status.lastSuccessAt))
      : m.aegis_status_never(),
    pending: String(status.pending),
  });
  return status.lastError ? `${details} ${status.lastError}` : details;
};

/** Whether Aegis Agent is reporting to Aegis Cloud, for the header. */
export const AegisConnection = () => {
  const status = useAegisStatus();
  if (!status) return null;
  const { label, color } = presentation[status.state] ?? presentation.disabled;

  return (
    <Tooltip content={aegisStatusDetails(status)}>
      <Flex
        align="center"
        gap={1.5}
        display={{ base: "none", md: "flex" }}
        data-testid="aegis-connection"
        data-state={status.state}
      >
        <Box w="8px" h="8px" borderRadius="full" bg={color} flexShrink={0} />
        <Text fontSize="xs" color="whiteAlpha.700" whiteSpace="nowrap">
          {label()}
        </Text>
      </Flex>
    </Tooltip>
  );
};
