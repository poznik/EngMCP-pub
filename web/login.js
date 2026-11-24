const ticket = location.hash.slice(1);
if (/^[a-f0-9]{48}$/.test(ticket)) {
  history.replaceState(null, "", "/");
  const form = document.getElementById("login");
  form.elements.ticket.value = ticket;
  form.submit();
}
