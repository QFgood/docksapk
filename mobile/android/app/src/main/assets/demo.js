const form = document.querySelector('#demo');
const message = document.querySelector('#message');
const receipt = document.querySelector('#receipt');
const key = 'docks-demo-message';

function show(value) {
  if (value) receipt.textContent = '演示表单已收到：' + value;
}

show(sessionStorage.getItem(key));
form.addEventListener('submit', event => {
  event.preventDefault();
  show(message.value);
  sessionStorage.setItem(key, message.value);
});
